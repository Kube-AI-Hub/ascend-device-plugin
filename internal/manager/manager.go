/*
 * Copyright 2024 The HAMi Authors.
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package manager

import (
	"fmt"
	"sort"
	"sync"

	"ascend-common/devmanager"
	"ascend-common/devmanager/common"
	"ascend-common/devmanager/dcmi"

	"github.com/Project-HAMi/ascend-device-plugin/internal"
	"k8s.io/klog/v2"
)

type Device struct {
	UUID     string
	LogicID  int32
	PhyID    int32
	CardID   int32
	DeviceID int32
	Memory   int64
	AICore   int32
	Health   bool
}

// Manager defines the interface that PluginServer depends on.
// AscendManager implements this interface.
type Manager interface {
	CommonWord() string
	ResourceName() string
	VDeviceCount() int
	UpdateDevice() error
	GetDevices() []*Device
	GetDeviceByUUID(UUID string) *Device
	GetUnHealthIDs() []int32
	CleanupIdleVNPUs() error
	IsHamiVnpuCore() bool
	// StaleRegisterCommonWords are other commonWords that share this node's
	// chipName. Their node-register/handshake annotations must be cleared so
	// HAMi does not see two SKUs on one node after a 24G/48G switch.
	StaleRegisterCommonWords() []string
}

type AscendManager struct {
	mu           sync.RWMutex
	mgr          devmanager.DeviceInterface
	config       internal.VNPUConfig
	globalConfig internal.Config
	devs         []*Device
	nodeConfig   *internal.NodeConfig
}

func NewAscendManager() (*AscendManager, error) {
	mgr, err := devmanager.AutoInit("", 30)
	if err != nil {
		return nil, fmt.Errorf("failed to auto-init device manager: %w", err)
	}
	return &AscendManager{
		mgr:  mgr,
		devs: []*Device{},
	}, nil
}

func (am *AscendManager) LoadNodeConfig(nodePath string, nodeName string) error {
	nodeConfigList, err := internal.LoadNodeConfig(nodePath)
	if err != nil {
		klog.Warningf("Failed to load node config from %s: %v", nodePath, err)
		return err
	}

	for _, n := range nodeConfigList.Nodes {
		if n.Name == nodeName {
			am.nodeConfig = &n
			klog.Infof("Successfully matched node config for %s: %+v", nodeName, n)
			return nil
		}
	}

	klog.Infof("No specific config found for node %s, will use default settings", nodeName)
	return nil
}

func (am *AscendManager) shouldIgnoreDevice(uuid string, index int32) bool {
	if !am.shouldCheckIgnored() {
		return false
	}

	for _, ignoredUUID := range am.nodeConfig.FilterDevices.UUID {
		if uuid != "" && uuid == ignoredUUID {
			return true
		}
	}
	for _, ignoredIndex := range am.nodeConfig.FilterDevices.Index {
		if index == ignoredIndex {
			return true
		}
	}
	return false
}

func (am *AscendManager) shouldCheckIgnored() bool {
	return am.nodeConfig != nil && !am.nodeConfig.FilterDevices.IsEmpty()
}

func (am *AscendManager) LoadConfig(path string) error {
	config, err := internal.LoadConfig(path)
	if err != nil {
		return fmt.Errorf("failed to load config from %s: %w", path, err)
	}
	chipInfo, err := am.mgr.GetValidChipInfo()
	if err != nil {
		return fmt.Errorf("failed to get valid chip info: %w", err)
	}
	if chipInfo.Type != "Ascend" {
		return fmt.Errorf("chip type is not Ascend")
	}
	devType := common.GetDeviceTypeByChipName(chipInfo.Name)
	memoryMB := probeDeviceMemoryMB(am.mgr)
	klog.Infof("detected chip name=%s, classified devType=%s, dcmiMemoryMiB=%d", chipInfo.Name, devType, memoryMB)
	idx := indexVNPUConfig(config.VNPUs.Configs, chipInfo.Name, devType, memoryMB)
	if idx == -1 {
		return fmt.Errorf("can not find vnpu config for chip %s (devType=%s, dcmiMemoryMiB=%d)", chipInfo.Name, devType, memoryMB)
	}
	am.config = config.VNPUs.Configs[idx]
	am.globalConfig = *config
	sort.Slice(am.config.Templates, func(i, j int) bool {
		return am.config.Templates[i].Memory < am.config.Templates[j].Memory
	})
	klog.Infof("load config: %v", am.config)
	return nil
}

func probeDeviceMemoryMB(mgr devmanager.DeviceInterface) uint64 {
	if mgr == nil {
		return 0
	}
	_, ids, err := mgr.GetDeviceList()
	if err != nil || len(ids) == 0 {
		klog.V(4).Infof("probe DCMI memory: no device list: %v", err)
		return 0
	}
	for _, id := range ids {
		info, err := mgr.GetDeviceMemoryInfo(id)
		if err != nil || info == nil || info.MemorySize == 0 {
			continue
		}
		return info.MemorySize
	}
	return 0
}

func hasMemoryBand(vnpu internal.VNPUConfig) bool {
	return vnpu.MemoryMatchMin > 0 || vnpu.MemoryMatchMax > 0
}

// configMatchesMemory reports whether memoryMB is in [min, max). A zero bound
// is unbounded. Unconstrained entries match any reading, including 0.
func configMatchesMemory(vnpu internal.VNPUConfig, memoryMB uint64) bool {
	if !hasMemoryBand(vnpu) {
		return true
	}
	if memoryMB == 0 {
		return false
	}
	if vnpu.MemoryMatchMin > 0 && memoryMB < uint64(vnpu.MemoryMatchMin) {
		return false
	}
	if vnpu.MemoryMatchMax > 0 && memoryMB >= uint64(vnpu.MemoryMatchMax) {
		return false
	}
	return true
}

func pickFromCandidates(configs []internal.VNPUConfig, idxs []int, memoryMB uint64) int {
	if len(idxs) == 0 {
		return -1
	}
	if len(idxs) == 1 {
		return idxs[0]
	}
	var unconstrained []int
	for _, i := range idxs {
		if hasMemoryBand(configs[i]) {
			if configMatchesMemory(configs[i], memoryMB) {
				return i
			}
			continue
		}
		unconstrained = append(unconstrained, i)
	}
	if len(unconstrained) > 0 {
		return unconstrained[0]
	}
	return idxs[0]
}

// indexVNPUConfig prefers an exact chipName hit, then DevType+chipName, then
// DevType alone. DCMI reports 910A as "910B" (no suffix); config entries can
// therefore set chipName: "910B" and/or devType: Ascend910.
//
// Multiple entries may share chipName (310P 24G vs 48G). When more than one
// matches, memoryMB (DCMI MiB) selects the entry whose [memoryMatchMin,
// memoryMatchMax) contains it. A failed DCMI read (0) prefers an unconstrained
// entry, then the first chipName hit.
func indexVNPUConfig(configs []internal.VNPUConfig, chipName, devType string, memoryMB uint64) int {
	var exact []int
	for i, vnpu := range configs {
		if vnpu.ChipName == chipName {
			exact = append(exact, i)
		}
	}
	if idx := pickFromCandidates(configs, exact, memoryMB); idx != -1 {
		return idx
	}
	var typed []int
	for i, vnpu := range configs {
		if vnpu.DevType != "" && vnpu.DevType == devType {
			if vnpu.ChipName == "" || vnpu.ChipName == chipName {
				typed = append(typed, i)
			}
		}
	}
	if idx := pickFromCandidates(configs, typed, memoryMB); idx != -1 {
		return idx
	}
	var byType []int
	for i, vnpu := range configs {
		if vnpu.DevType != "" && vnpu.DevType == devType {
			byType = append(byType, i)
		}
	}
	return pickFromCandidates(configs, byType, memoryMB)
}

func (am *AscendManager) CommonWord() string {
	return am.config.CommonWord
}

func (am *AscendManager) StaleRegisterCommonWords() []string {
	var out []string
	for _, c := range am.globalConfig.VNPUs.Configs {
		if c.CommonWord == "" || c.CommonWord == am.config.CommonWord {
			continue
		}
		if c.ChipName != "" && c.ChipName == am.config.ChipName {
			out = append(out, c.CommonWord)
		}
	}
	return out
}

func (am *AscendManager) ResourceName() string {
	return am.config.ResourceName
}

func (am *AscendManager) VDeviceCount() int {
	// Prefer the per-node override when present, mirroring IsHamiVnpuCore().
	if am.nodeConfig != nil && am.nodeConfig.VDeviceCount > 0 {
		return am.nodeConfig.VDeviceCount
	}
	if len(am.config.Templates) == 0 {
		return 1
	}
	return int(am.config.MemoryAllocatable / am.config.Templates[0].Memory)
}

func (am *AscendManager) UpdateDevice() error {
	_, IDs, err := am.mgr.GetDeviceList()
	if err != nil {
		klog.Errorf("failed to get device list: %v", err)
		return err
	}

	newDevs := make([]*Device, 0, len(IDs))
	for _, ID := range IDs {
		phyID, err := am.mgr.GetPhysicIDFromLogicID(ID)
		if err != nil {
			klog.Errorf("failed to get physic id from logic id: %v", err)
			return err
		}
		cardID, deviceID, err := am.mgr.GetCardIDDeviceID(ID)
		if err != nil {
			klog.Errorf("failed to get card id from device id: %v", err)
			return err
		}
		uuid, err := am.mgr.GetDieID(ID, dcmi.VDIE)
		if err != nil {
			klog.Errorf("failed to get uuid from device id: %v", err)
			return err
		}
		if am.shouldIgnoreDevice(uuid, cardID) {
			klog.V(4).Infof("ignore device matched filterDevices uuid=%s index=%d logicID=%d phyID=%d deviceID=%d", uuid, cardID, ID, phyID, deviceID)
			continue
		}
		health, err := am.mgr.GetDeviceHealth(ID)
		if err != nil {
			klog.Errorf("failed to get device health: %v", err)
			return err
		}
		newDevs = append(newDevs, &Device{
			UUID:     uuid,
			LogicID:  ID,
			PhyID:    phyID,
			CardID:   cardID,
			DeviceID: deviceID,
			Memory:   am.config.MemoryAllocatable,
			AICore:   am.config.AICore,
			Health:   health == 0,
		})
	}
	am.mu.Lock()
	am.devs = newDevs
	am.mu.Unlock()
	return nil
}

func (am *AscendManager) GetDevices() []*Device {
	am.mu.RLock()
	defer am.mu.RUnlock()
	return am.devs
}

func (am *AscendManager) GetDeviceByUUID(UUID string) *Device {
	am.mu.RLock()
	defer am.mu.RUnlock()
	for _, dev := range am.devs {
		if dev.UUID == UUID {
			return dev
		}
	}
	return nil
}

func (am *AscendManager) GetIDs() []int32 {
	_, IDs, err := am.mgr.GetDeviceList()
	if err != nil {
		klog.Errorf("failed to get device list: %v", err)
		return nil
	}
	if !am.shouldCheckIgnored() {
		return IDs
	}

	availableIDs := make([]int32, 0, len(IDs))
	for _, id := range IDs {
		cardID, _, err := am.mgr.GetCardIDDeviceID(id)
		if err != nil {
			klog.Warningf("failed to get card/device ID for logic ID %d: %v", id, err)
			continue
		}
		uuid := ""
		if am.nodeConfig.FilterDevices.HasUUID() {
			uuid, err = am.mgr.GetDieID(id, dcmi.VDIE)
			if err != nil {
				klog.Warningf("failed to get uuid for logic ID %d: %v", id, err)
				continue
			}
		}
		if !am.shouldIgnoreDevice(uuid, cardID) {
			availableIDs = append(availableIDs, id)
		}
	}
	return availableIDs
}

func (am *AscendManager) GetUnHealthIDs() []int32 {
	_, IDs, err := am.mgr.GetDeviceList()
	if err != nil {
		return nil
	}
	filterDevicesEnabled := am.shouldCheckIgnored()
	var unhealthy []int32
	for _, d := range IDs {
		if filterDevicesEnabled {
			cardID, _, err := am.mgr.GetCardIDDeviceID(d)
			if err != nil {
				klog.Warningf("failed to get card/device ID for logic ID %d: %v", d, err)
				continue
			}
			uuid := ""
			if am.nodeConfig.FilterDevices.HasUUID() {
				uuid, err = am.mgr.GetDieID(d, dcmi.VDIE)
				if err != nil {
					klog.Warningf("failed to get uuid for logic ID %d: %v", d, err)
					continue
				}
			}
			if am.shouldIgnoreDevice(uuid, cardID) {
				continue
			}
		}
		healthCode, err := am.mgr.GetDeviceHealth(d)
		if err != nil {
			klog.Warningf("failed to get device health for %d: %v", d, err)
			continue
		}
		if healthCode != 0 {
			unhealthy = append(unhealthy, d)
		}
	}
	return unhealthy
}

func (am *AscendManager) CleanupIdleVNPUs() error {
	klog.Info("Starting cleanup of idle vNPUs...")

	_, IDs, err := am.mgr.GetDeviceList()
	if err != nil {
		return fmt.Errorf("failed to get device list: %w", err)
	}
	klog.Infof("Found %d devices to check for idle vNPUs,%+v", len(IDs), IDs)

	totalCleaned := 0
	for _, logicID := range IDs {
		cardID, deviceID, err := am.mgr.GetCardIDDeviceID(logicID)
		if err != nil {
			klog.Warningf("failed to get card/device ID for logic ID %d: %v", logicID, err)
			continue
		}
		uuid := ""
		if am.shouldCheckIgnored() && am.nodeConfig.FilterDevices.HasUUID() {
			uuid, err = am.mgr.GetDieID(logicID, dcmi.VDIE)
			if err != nil {
				klog.Warningf("failed to get uuid for logic ID %d: %v", logicID, err)
				continue
			}
		}
		if am.shouldIgnoreDevice(uuid, cardID) {
			klog.V(4).Infof("skip cleanup on ignored device uuid=%s index=%d logicID=%d deviceID=%d", uuid, cardID, logicID, deviceID)
			continue
		}
		// Obtain all vNPU information on this device
		vDevInfos, err := am.mgr.GetVirtualDeviceInfo(logicID)
		if err != nil {
			klog.Infof("no vNPU found on device %d or query failed: %v", logicID, err)
			continue
		}

		klog.V(1).Infof("Device logicID=%d, cardID=%d,deviceID=%d has %d vNPUs", logicID, cardID, deviceID, len(vDevInfos.VDevInfo))

		for _, vDev := range vDevInfos.VDevInfo {
			klog.V(1).Infof("vNPU CardId=%d, VDevID(Vnpu ID)=%d,template=%s,IsContainerUsed=%d", cardID, vDev.VDevID, vDev.QueryInfo.Name, vDev.QueryInfo.IsContainerUsed)

			if vDev.QueryInfo.IsContainerUsed == 0 {
				klog.V(1).Infof("Found idle vNPU: cardID=%d, deviceID=%d, vnpuID=%d, status=%d, template=%s,IsContainerUsed=%d",
					cardID, deviceID, vDev.VDevID, vDev.QueryInfo.Status, vDev.QueryInfo.Name, vDev.QueryInfo.IsContainerUsed)

				err := am.mgr.DestroyVirtualDevice(logicID, uint32(vDev.VDevID))
				if err != nil {
					klog.Errorf("failed to destroy vNPU %d on device %d: %v", vDev.VDevID, logicID, err)
				} else {
					klog.Infof("Successfully destroyed idle vNPU: vnpuID=%d", vDev.VDevID)
					totalCleaned++
				}
			} else {
				klog.Infof("Skipping active vNPU: cardID=%d, deviceID=%d, vnpuID=%d, status=%d, template=%s,IsContainerUsed=%d",
					cardID, deviceID, vDev.VDevID, vDev.QueryInfo.Status, vDev.QueryInfo.Name, vDev.QueryInfo.IsContainerUsed)
			}
		}
	}

	klog.Infof("Cleanup completed, destroyed %d idle vNPUs", totalCleaned)
	return nil
}

func (am *AscendManager) GetNodeConfig() *internal.NodeConfig {
	return am.nodeConfig
}

func (am *AscendManager) IsHamiVnpuCore() bool {
	if am.nodeConfig != nil {
		return am.nodeConfig.HamiVnpuCore
	}
	return am.globalConfig.VNPUs.HamiVnpuCore
}
