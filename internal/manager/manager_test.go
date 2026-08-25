/*
 * Copyright 2026 The HAMi Authors.
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
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ascend-common/devmanager"
	"ascend-common/devmanager/common"

	"github.com/Project-HAMi/ascend-device-plugin/internal"
)

// TestVDeviceCount verifies that VDeviceCount honors a per-node override
// (nodeConfig.VDeviceCount) and otherwise falls back to the global default.
func TestVDeviceCount(t *testing.T) {
	// A representative Ascend910B4 config: 32768 MiB allocatable, smallest
	// template 8192 MiB -> global default = 32768/8192 = 4 vDevices/card.
	cfg910B4 := internal.VNPUConfig{
		CommonWord:        "Ascend910B4",
		MemoryAllocatable: 32768,
		Templates: []internal.Template{
			{Name: "vir03_1c_8g", Memory: 8192, AICore: 5},
			{Name: "vir06_1c_16g", Memory: 16384, AICore: 10},
			{Name: "vir12_3c_32g", Memory: 32768, AICore: 20},
		},
	}

	tests := []struct {
		name       string
		config     internal.VNPUConfig
		nodeConfig *internal.NodeConfig
		want       int
	}{
		{
			name:       "no node config -> global default (32768/8192=4)",
			config:     cfg910B4,
			nodeConfig: nil,
			want:       4,
		},
		{
			name:       "node config VDeviceCount=0 -> falls back to global default",
			config:     cfg910B4,
			nodeConfig: &internal.NodeConfig{Name: "node-001", VDeviceCount: 0},
			want:       4,
		},
		{
			name:       "node config VDeviceCount=8 -> honored (the fix)",
			config:     cfg910B4,
			nodeConfig: &internal.NodeConfig{Name: "node-001", VDeviceCount: 8},
			want:       8,
		},
		{
			name:       "node config VDeviceCount=2 -> honored, tracks the value",
			config:     cfg910B4,
			nodeConfig: &internal.NodeConfig{Name: "node-001", VDeviceCount: 2},
			want:       2,
		},
		{
			name:       "no templates, no node config -> 1",
			config:     internal.VNPUConfig{CommonWord: "AscendX", MemoryAllocatable: 32768},
			nodeConfig: nil,
			want:       1,
		},
		{
			name:       "no templates but node override present -> node override wins",
			config:     internal.VNPUConfig{CommonWord: "AscendX", MemoryAllocatable: 32768},
			nodeConfig: &internal.NodeConfig{Name: "node-001", VDeviceCount: 5},
			want:       5,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			am := &AscendManager{
				config:     tt.config,
				nodeConfig: tt.nodeConfig,
			}
			if got := am.VDeviceCount(); got != tt.want {
				t.Fatalf("VDeviceCount() = %d, want %d", got, tt.want)
			}
		})
	}
}

// chipInfoDeviceManager reports a fixed chip so LoadConfig can pick an entry
// without a real NPU.
type chipInfoDeviceManager struct {
	*devmanager.DeviceManagerMock
	chipName   string
	memorySize uint64
	memoryErr  error
}

func (d *chipInfoDeviceManager) GetValidChipInfo() (common.ChipInfo, error) {
	return common.ChipInfo{Type: "Ascend", Name: d.chipName}, nil
}

func (d *chipInfoDeviceManager) GetDeviceList() (int32, []int32, error) {
	return 1, []int32{0}, nil
}

func (d *chipInfoDeviceManager) GetDeviceMemoryInfo(logicID int32) (*common.MemoryInfo, error) {
	if d.memoryErr != nil {
		return nil, d.memoryErr
	}
	return &common.MemoryInfo{MemorySize: d.memorySize}, nil
}

func writeConfig(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "device-config.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

// TestLoadConfigAcrossHAMiVersions checks that the manager resolves its own
// chip out of a multi-chip list in either layout, so that upgrading the plugin
// ahead of HAMi does not break startup.
func TestLoadConfigAcrossHAMiVersions(t *testing.T) {
	// Two chips so that picking the right entry, not just the first one, is
	// actually exercised. 910B3: 65536/16384 = 4 vDevices; 910B4: 32768/8192 = 4.
	const chips = `
- chipName: 910B3
  commonWord: Ascend910B3
  resourceName: huawei.com/Ascend910B3
  memoryAllocatable: 65536
  templates:
  - {name: vir10_3c_32g, memory: 32768}
  - {name: vir05_1c_16g, memory: 16384}
- chipName: 910B4
  commonWord: Ascend910B4
  resourceName: huawei.com/Ascend910B4
  memoryAllocatable: 32768
  templates:
  - {name: vir05_1c_8g, memory: 8192}
`
	// The same chips under `vnpus.configs:` need one more level of indentation.
	currentLayout := "vnpus:\n  hamiVnpuCore: true\n  configs:" + strings.ReplaceAll(chips, "\n", "\n  ")
	legacyLayout := "vnpus:" + chips

	tests := []struct {
		name             string
		content          string
		chipName         string
		wantCommonWord   string
		wantResourceName string
		wantHamiVnpuCore bool
	}{
		{
			name:             "current layout",
			content:          currentLayout,
			chipName:         "910B3",
			wantCommonWord:   "Ascend910B3",
			wantResourceName: "huawei.com/Ascend910B3",
			wantHamiVnpuCore: true,
		},
		{
			name:             "legacy layout, second entry",
			content:          legacyLayout,
			chipName:         "910B4",
			wantCommonWord:   "Ascend910B4",
			wantResourceName: "huawei.com/Ascend910B4",
			wantHamiVnpuCore: false,
		},
		{
			name:             "legacy layout with top-level hamiVnpuCore",
			content:          "hamiVnpuCore: true\n" + legacyLayout,
			chipName:         "910B3",
			wantCommonWord:   "Ascend910B3",
			wantResourceName: "huawei.com/Ascend910B3",
			wantHamiVnpuCore: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			am := &AscendManager{mgr: &chipInfoDeviceManager{chipName: tt.chipName}}
			if err := am.LoadConfig(writeConfig(t, tt.content)); err != nil {
				t.Fatalf("LoadConfig() error = %v, want nil", err)
			}
			if got := am.CommonWord(); got != tt.wantCommonWord {
				t.Errorf("CommonWord() = %q, want %q", got, tt.wantCommonWord)
			}
			if got := am.ResourceName(); got != tt.wantResourceName {
				t.Errorf("ResourceName() = %q, want %q", got, tt.wantResourceName)
			}
			if got := am.VDeviceCount(); got != 4 {
				t.Errorf("VDeviceCount() = %d, want 4", got)
			}
			if got := am.IsHamiVnpuCore(); got != tt.wantHamiVnpuCore {
				t.Errorf("IsHamiVnpuCore() = %v, want %v", got, tt.wantHamiVnpuCore)
			}
		})
	}
}

// TestLoadConfigChipNotFound keeps the "no entry for this chip" path distinct
// from a parse failure in both layouts.
func TestLoadConfigChipNotFound(t *testing.T) {
	path := writeConfig(t, "vnpus:\n- chipName: 910B3\n  commonWord: Ascend910B3\n")
	am := &AscendManager{mgr: &chipInfoDeviceManager{chipName: "310P3"}}
	err := am.LoadConfig(path)
	if err == nil {
		t.Fatal("LoadConfig() error = nil, want an error")
	}
	if !strings.Contains(err.Error(), "310P3") {
		t.Errorf("LoadConfig() error = %v, want it to name the missing chip", err)
	}
}

const dual310PConfig = `vnpus:
  configs:
  - chipName: 310P3
    devType: Ascend310P
    commonWord: Ascend310P
    resourceName: huawei.com/Ascend310P
    memoryAllocatable: 21527
    memoryCapacity: 24576
    memoryMatchMax: 32768
    templates:
    - {name: vir01, memory: 3072}
  - chipName: 310P3
    devType: Ascend310P
    commonWord: Ascend310P48
    resourceName: huawei.com/Ascend310P48
    memoryAllocatable: 43054
    memoryCapacity: 44278
    memoryMatchMin: 32768
    templates:
    - {name: vir01, memory: 6144}
`

func TestIndexVNPUConfigMemorySKU(t *testing.T) {
	am := &AscendManager{mgr: &chipInfoDeviceManager{chipName: "310P3", memorySize: 44278}}
	if err := am.LoadConfig(writeConfig(t, dual310PConfig)); err != nil {
		t.Fatalf("LoadConfig 48G: %v", err)
	}
	if got := am.CommonWord(); got != "Ascend310P48" {
		t.Fatalf("48G CommonWord() = %q, want Ascend310P48", got)
	}
	if got := am.ResourceName(); got != "huawei.com/Ascend310P48" {
		t.Fatalf("48G ResourceName() = %q", got)
	}
	if stale := am.StaleRegisterCommonWords(); len(stale) != 1 || stale[0] != "Ascend310P" {
		t.Fatalf("48G StaleRegisterCommonWords() = %v, want [Ascend310P]", stale)
	}

	am24 := &AscendManager{mgr: &chipInfoDeviceManager{chipName: "310P3", memorySize: 22100}}
	if err := am24.LoadConfig(writeConfig(t, dual310PConfig)); err != nil {
		t.Fatalf("LoadConfig 24G: %v", err)
	}
	if got := am24.CommonWord(); got != "Ascend310P" {
		t.Fatalf("24G CommonWord() = %q, want Ascend310P", got)
	}

	amFail := &AscendManager{mgr: &chipInfoDeviceManager{chipName: "310P3", memoryErr: os.ErrNotExist}}
	if err := amFail.LoadConfig(writeConfig(t, dual310PConfig)); err != nil {
		t.Fatalf("LoadConfig DCMI fail: %v", err)
	}
	if got := amFail.CommonWord(); got != "Ascend310P" {
		t.Fatalf("DCMI fail CommonWord() = %q, want first/24G entry Ascend310P", got)
	}
}

func TestIndexVNPUConfigSingleHitIgnoresMemory(t *testing.T) {
	configs := []internal.VNPUConfig{
		{ChipName: "910B3", CommonWord: "Ascend910B3"},
		{ChipName: "310P3", CommonWord: "Ascend310P", MemoryMatchMax: 32768},
	}
	if got := indexVNPUConfig(configs, "910B3", "Ascend910B", 44278); got != 0 {
		t.Fatalf("910B3 idx = %d, want 0", got)
	}
}
