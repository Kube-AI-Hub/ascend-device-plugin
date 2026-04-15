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

package server

import (
	"context"
	"flag"
	"fmt"
	"net"
	"os"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/Project-HAMi/HAMi/pkg/device"
	"github.com/Project-HAMi/HAMi/pkg/device/ascend"
	"github.com/Project-HAMi/HAMi/pkg/util"
	"github.com/Project-HAMi/HAMi/pkg/util/nodelock"
	"github.com/Project-HAMi/ascend-device-plugin/internal/manager"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/keepalive"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/json"
	"k8s.io/klog/v2"
	"k8s.io/kubelet/pkg/apis/deviceplugin/v1beta1"
)

const (
	// RegisterAnnos = "hami.io/node-register-ascend"
	// PodAllocAnno = "huawei.com/AscendDevices"
	NodeLockAscend  = "hami.io/mutex.lock"
	Ascend910Prefix = "Ascend910"
	Ascend910CType  = "Ascend910C"

	// gRPC server tuning (aligned with mind-cluster ascend-device-plugin/pkg/common)
	maxGRPCRecvMsgSize         = 4 * 1024 * 1024
	maxGRPCConcurrentStreams   = 64
	devicePluginSocketFileMode = 0600
)

var grpcKeepAlive = keepalive.ServerParameters{
	Time:    5 * time.Minute,
	Timeout: 5 * time.Minute,
}

var (
	reportTimeOffset = flag.Int64("report_time_offset", 1, "report time offset")
)

type PluginServer struct {
	nodeName               string
	registerAnno           string
	handshakeAnno          string
	allocAnno              string
	grpcServer             *grpc.Server
	mgr                    *manager.AscendManager
	socket                 string
	stopCh                 chan interface{}
	stopMu                 sync.Mutex
	stopped                bool
	healthCh               chan int32
	checkIdleVNPUInterval  int
	hamiRegisterIntervalSec int
}

func NewPluginServer(mgr *manager.AscendManager, nodeName string, checkIdleVNPUInterval, hamiRegisterIntervalSec int) (*PluginServer, error) {
	return &PluginServer{
		nodeName:                nodeName,
		registerAnno:            fmt.Sprintf("hami.io/node-register-%s", mgr.CommonWord()),
		handshakeAnno:           fmt.Sprintf("hami.io/node-handshake-%s", mgr.CommonWord()),
		allocAnno:               fmt.Sprintf("huawei.com/%s", mgr.CommonWord()),
		grpcServer:              nil,
		mgr:                     mgr,
		socket:                  path.Join(v1beta1.DevicePluginPath, fmt.Sprintf("%s.sock", mgr.CommonWord())),
		stopCh:                  nil,
		healthCh:                make(chan int32, 1),
		checkIdleVNPUInterval:   checkIdleVNPUInterval,
		hamiRegisterIntervalSec: hamiRegisterIntervalSec,
	}, nil
}

func (ps *PluginServer) Start() error {
	ps.stopMu.Lock()
	ps.stopCh = make(chan interface{})
	ps.stopped = false
	ps.stopMu.Unlock()
	err := ps.mgr.UpdateDevice()
	if err != nil {
		return err
	}
	err = ps.serve()
	if err != nil {
		return err
	}
	err = ps.registerKubelet()
	if err != nil {
		return err
	}
	go ps.startPeriodicCheckIdleVNPUs()
	go ps.watchAndRegister()
	return nil
}

func (ps *PluginServer) startPeriodicCheckIdleVNPUs() {
	ticker := time.NewTicker(time.Duration(ps.checkIdleVNPUInterval) * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			klog.Info("Running scheduled idle vNPU cleanup")
			if err := ps.CleanupIdleVNPUs(); err != nil {
				klog.Errorf("Failed to cleanup idle vNPUs: %v", err)
			}
		case <-ps.stopCh:
			klog.Info("Stopping cleanup goroutine")
			return
		}
	}
}

func (ps *PluginServer) Stop() error {
	ps.stopMu.Lock()
	defer ps.stopMu.Unlock()
	if ps.stopped {
		return nil
	}
	ps.stopped = true
	if ps.stopCh != nil {
		close(ps.stopCh)
	}
	if ps.grpcServer != nil {
		ps.grpcServer.Stop()
		ps.grpcServer = nil
	}
	return nil
}

func (ps *PluginServer) StopCh() <-chan interface{} {
	return ps.stopCh
}

func (ps *PluginServer) CleanupIdleVNPUs() error {
	return ps.mgr.CleanupIdleVNPUs()
}

func (ps *PluginServer) dial(unixSocketPath string, timeout time.Duration) (*grpc.ClientConn, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	c, err := grpc.DialContext(ctx, unixSocketPath,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithBlock(),
		grpc.WithContextDialer(func(ctx2 context.Context, addr string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx2, "unix", addr)
		}),
	)

	if err != nil {
		return nil, err
	}
	return c, nil
}

func (ps *PluginServer) serve() error {
	_ = os.Remove(ps.socket)
	sock, err := net.Listen("unix", ps.socket)
	if err != nil {
		return err
	}
	if err := os.Chmod(ps.socket, devicePluginSocketFileMode); err != nil {
		_ = sock.Close()
		return fmt.Errorf("chmod device plugin socket: %w", err)
	}
	ps.grpcServer = grpc.NewServer(
		grpc.MaxRecvMsgSize(maxGRPCRecvMsgSize),
		grpc.MaxConcurrentStreams(maxGRPCConcurrentStreams),
		grpc.KeepaliveParams(grpcKeepAlive),
	)
	v1beta1.RegisterDevicePluginServer(ps.grpcServer, ps)
	resourceName := ps.mgr.ResourceName()
	go func() {
		klog.Infof("Starting GRPC server for '%s'", resourceName)
		if err := ps.grpcServer.Serve(sock); err != nil {
			klog.Errorf("GRPC server for '%s' exited: %v", resourceName, err)
		}
	}()

	// Wait for server to start by launching a blocking connexion
	conn, err := ps.dial(ps.socket, 5*time.Second)
	if err != nil {
		return err
	}
	_ = conn.Close()

	return nil
}

func (ps *PluginServer) registerKubelet() error {
	conn, err := ps.dial(v1beta1.KubeletSocket, 5*time.Second)
	if err != nil {
		return err
	}
	defer func(conn *grpc.ClientConn) {
		_ = conn.Close()
	}(conn)
	client := v1beta1.NewRegistrationClient(conn)
	reqt := &v1beta1.RegisterRequest{
		Version:      v1beta1.Version,
		Endpoint:     path.Base(ps.socket),
		ResourceName: ps.mgr.ResourceName(),
		Options: &v1beta1.DevicePluginOptions{
			GetPreferredAllocationAvailable: false,
		},
	}

	_, err = client.Register(context.Background(), reqt)
	if err != nil {
		return err
	}
	return nil
}

func (ps *PluginServer) getDeviceNetworkID(idx int, deviceType string) (int, error) {
	// For Ascend910C devices, all modules (dies) are interconnected via HCCS
	if deviceType == Ascend910CType {
		return 0, nil
	}

	if idx > 3 {
		return 1, nil
	}

	return 0, nil
}

func (ps *PluginServer) registerHAMi() error {
	devs := ps.mgr.GetDevices()
	apiDevices := make([]*device.DeviceInfo, 0, len(devs))
	// hami currently believes that the index starts from 0 and is continuous.
	for i, dev := range devs {
		device := &device.DeviceInfo{
			Index:   uint(i),
			ID:      dev.UUID,
			Count:   int32(ps.mgr.VDeviceCount()),
			Devmem:  int32(dev.Memory),
			Devcore: dev.AICore,
			Type:    ps.mgr.CommonWord(),
			Numa:    0,
			Health:  dev.Health,
		}
		if strings.HasPrefix(device.Type, Ascend910Prefix) {
			NetworkID, err := ps.getDeviceNetworkID(i, device.Type)
			if err != nil {
				return fmt.Errorf("get networkID error: %v", err)
			}
			device.CustomInfo = map[string]any{
				"NetworkID": NetworkID,
			}
		}
		apiDevices = append(apiDevices, device)
	}
	annos := make(map[string]string)
	annos[ps.registerAnno] = device.MarshalNodeDevices(apiDevices)
	annos[ps.handshakeAnno] = "Reported_" + time.Now().Add(time.Duration(*reportTimeOffset)*time.Second).Format("2006.01.02 15:04:05")
	node, err := util.GetNode(ps.nodeName)
	if err != nil {
		return fmt.Errorf("get node %s error: %v", ps.nodeName, err)
	}
	err = util.PatchNodeAnnotations(node, annos)
	if err != nil {
		return fmt.Errorf("patch node %s annotations error: %v", ps.nodeName, err)
	}
	klog.V(5).Infof("patch node %s annotations: %v", ps.nodeName, annos)
	return nil
}

func (ps *PluginServer) watchAndRegister() {
	okInterval := time.Duration(ps.hamiRegisterIntervalSec) * time.Second
	if okInterval < 3*time.Second {
		okInterval = 3 * time.Second
	}
	timer := time.After(1 * time.Second)
	for {
		select {
		case <-ps.stopCh:
			klog.Infof("stop watch and register")
			return
		case <-timer:
		}
		unhealthy := ps.mgr.GetUnHealthIDs()
		if len(unhealthy) > 0 {
			if err := ps.mgr.UpdateDevice(); err != nil {
				klog.Errorf("update device error: %v", err)
				timer = time.After(5 * time.Second)
				continue
			}
			select {
			case ps.healthCh <- unhealthy[0]:
			default:
			}
		}
		err := ps.registerHAMi()
		if err != nil {
			klog.Errorf("register HAMi error: %v", err)
			timer = time.After(5 * time.Second)
		} else {
			klog.V(3).Infof("register HAMi success")
			timer = time.After(okInterval)
		}
	}
}

func (ps *PluginServer) parsePodAnnotation(pod *v1.Pod) ([]int32, []string, error) {
	anno, ok := pod.Annotations[ps.allocAnno]
	if !ok {
		return nil, nil, fmt.Errorf("annotation %s not set", ps.allocAnno)
	}
	var rtInfo []ascend.RuntimeInfo
	err := json.Unmarshal([]byte(anno), &rtInfo)
	if err != nil {
		return nil, nil, fmt.Errorf("annotation %s value %s invalid", ps.allocAnno, anno)
	}
	var IDs []int32
	var temps []string
	for _, info := range rtInfo {
		if info.UUID == "" {
			continue
		}
		d := ps.mgr.GetDeviceByUUID(info.UUID)
		if d == nil {
			return nil, nil, fmt.Errorf("unknown uuid: %s", info.UUID)
		}
		IDs = append(IDs, d.PhyID)
		temps = append(temps, info.Temp)
	}
	if len(IDs) == 0 {
		return nil, nil, fmt.Errorf("annotation %s value %s invalid", ps.allocAnno, anno)
	}
	return IDs, temps, nil
}

func (ps *PluginServer) apiDevices() []*v1beta1.Device {
	devs := ps.mgr.GetDevices()
	devices := make([]*v1beta1.Device, 0, len(devs))
	vCount := ps.mgr.VDeviceCount()
	for _, dev := range devs {
		health := v1beta1.Unhealthy
		if dev.Health {
			health = v1beta1.Healthy
		}
		for i := 0; i < vCount; i++ {
			device := v1beta1.Device{
				ID:     fmt.Sprintf("%s-%d", dev.UUID, i),
				Health: health,
			}
			devices = append(devices, &device)
		}
	}
	klog.V(5).Infof("api devices: %v", devices)
	return devices
}

func (ps *PluginServer) GetDevicePluginOptions(context.Context, *v1beta1.Empty) (*v1beta1.DevicePluginOptions, error) {
	return &v1beta1.DevicePluginOptions{}, nil
}

func (ps *PluginServer) ListAndWatch(e *v1beta1.Empty, s v1beta1.DevicePlugin_ListAndWatchServer) error {
	_ = s.Send(&v1beta1.ListAndWatchResponse{Devices: ps.apiDevices()})
	for {
		select {
		case <-ps.stopCh:
			return nil
		case <-ps.healthCh:
			_ = s.Send(&v1beta1.ListAndWatchResponse{Devices: ps.apiDevices()})
		}
	}
}

func (ps *PluginServer) GetPreferredAllocation(context.Context, *v1beta1.PreferredAllocationRequest) (*v1beta1.PreferredAllocationResponse, error) {
	return nil, fmt.Errorf("not supported")
}

func checkAllocateRequest(requests *v1beta1.AllocateRequest) error {
	if requests == nil {
		return fmt.Errorf("allocate request is nil")
	}
	if len(requests.ContainerRequests) == 0 {
		return fmt.Errorf("empty container requests")
	}
	for i, r := range requests.ContainerRequests {
		if r == nil {
			return fmt.Errorf("nil container allocate request at index %d", i)
		}
		if len(r.DevicesIDs) == 0 {
			return fmt.Errorf("empty device IDs in container request at index %d", i)
		}
	}
	return nil
}

func (ps *PluginServer) Allocate(ctx context.Context, reqs *v1beta1.AllocateRequest) (*v1beta1.AllocateResponse, error) {
	klog.V(5).Infof("Allocate: %v", reqs)
	if err := checkAllocateRequest(reqs); err != nil {
		return nil, err
	}
	success := false
	var pod *v1.Pod
	defer func() {
		lockerr := nodelock.ReleaseNodeLock(ps.nodeName, NodeLockAscend, pod, success)
		if lockerr != nil {
			klog.Errorf("failed to release lock:%s", lockerr.Error())
		}
	}()
	pod, err := util.GetPendingPod(ctx, ps.nodeName)
	if err != nil {
		klog.Errorf("get pending pod error: %v", err)
		return nil, fmt.Errorf("get pending pod error: %v", err)
	}
	IDs, temps, err := ps.parsePodAnnotation(pod)
	if err != nil {
		return nil, fmt.Errorf("parse pod annotation error: %v", err)
	}
	if len(IDs) == 0 {
		return nil, fmt.Errorf("empty id from pod annotation")
	}
	ascendVisibleDevices := fmt.Sprintf("%d", IDs[0])
	ascendVNPUSpec := ""
	for i := 1; i < len(IDs); i++ {
		ascendVisibleDevices = fmt.Sprintf("%s,%d", ascendVisibleDevices, IDs[i])
	}
	for i := 0; i < len(temps); i++ {
		if temps[i] != "" {
			ascendVNPUSpec = temps[i]
			break
		}
	}
	resps := &v1beta1.AllocateResponse{}
	for range reqs.ContainerRequests {
		resp := v1beta1.ContainerAllocateResponse{
			Envs: map[string]string{
				"ASCEND_VISIBLE_DEVICES": ascendVisibleDevices,
			},
		}
		if ascendVNPUSpec != "" {
			resp.Envs["ASCEND_VNPU_SPECS"] = ascendVNPUSpec
		}
		resps.ContainerResponses = append(resps.ContainerResponses, &resp)
	}
	klog.Infof("Allocate: ASCEND_VISIBLE_DEVICES=%s, ASCEND_VNPU_SPECS=%s", ascendVisibleDevices, ascendVNPUSpec)
	success = true
	return resps, nil
}

func (ps *PluginServer) PreStartContainer(context.Context, *v1beta1.PreStartContainerRequest) (*v1beta1.PreStartContainerResponse, error) {
	return &v1beta1.PreStartContainerResponse{}, nil
}
