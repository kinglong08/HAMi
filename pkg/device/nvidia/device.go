package nvidia

import (
	"errors"
	"flag"
	"fmt"
	"strconv"
	"strings"

	"github.com/Project-HAMi/HAMi/pkg/api"
	"github.com/Project-HAMi/HAMi/pkg/scheduler/config"
	"github.com/Project-HAMi/HAMi/pkg/util"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/klog/v2"
)

const (
	HandshakeAnnos      = "hami.io/node-handshake"
	RegisterAnnos       = "hami.io/node-nvidia-register"
	NvidiaGPUDevice     = "NVIDIA"
	NvidiaGPUCommonWord = "GPU"
	GPUInUse            = "nvidia.com/use-gputype"
	GPUNoUse            = "nvidia.com/nouse-gputype"
	NumaBind            = "nvidia.com/numa-bind"
	// GPUUseUUID is user can use specify GPU device for set GPU UUID.
	GPUUseUUID = "nvidia.com/use-gpuuuid"
	// GPUNoUseUUID is user can not use specify GPU device for set GPU UUID.
	GPUNoUseUUID = "nvidia.com/nouse-gpuuuid"
)

var (
	ResourceName          string
	ResourceMem           string
	ResourceCores         string
	ResourceMemPercentage string
	ResourcePriority      string
	DebugMode             bool
)

type NvidiaGPUDevices struct {
}

func InitNvidiaDevice() *NvidiaGPUDevices {
	util.InRequestDevices[NvidiaGPUDevice] = "hami.io/vgpu-devices-to-allocate"
	util.SupportDevices[NvidiaGPUDevice] = "hami.io/vgpu-devices-allocated"
	util.HandshakeAnnos[NvidiaGPUDevice] = HandshakeAnnos
	return &NvidiaGPUDevices{}
}

func (dev *NvidiaGPUDevices) ParseConfig(fs *flag.FlagSet) {
	fs.StringVar(&ResourceName, "resource-name", "nvidia.com/gpu", "resource name")
	fs.StringVar(&ResourceMem, "resource-mem", "nvidia.com/gpumem", "gpu memory to allocate")
	fs.StringVar(&ResourceMemPercentage, "resource-mem-percentage", "nvidia.com/gpumem-percentage", "gpu memory fraction to allocate")
	fs.StringVar(&ResourceCores, "resource-cores", "nvidia.com/gpucores", "cores percentage to use")
	fs.StringVar(&ResourcePriority, "resource-priority", "vgputaskpriority", "vgpu task priority 0 for high and 1 for low")
}

func (dev *NvidiaGPUDevices) NodeCleanUp(nn string) error {
	return util.MarkAnnotationsToDelete(NvidiaGPUDevice, nn)
}

func (dev *NvidiaGPUDevices) CheckHealth(devType string, n *corev1.Node) (bool, bool) {
	return util.CheckHealth(devType, n)
}

func (dev *NvidiaGPUDevices) GetNodeDevices(n corev1.Node) ([]*api.DeviceInfo, error) {
	devEncoded, ok := n.Annotations[RegisterAnnos]
	if !ok {
		return []*api.DeviceInfo{}, errors.New("annos not found " + RegisterAnnos)
	}
	nodedevices, err := util.DecodeNodeDevices(devEncoded)
	if err != nil {
		klog.ErrorS(err, "failed to decode node devices", "node", n.Name, "device annotation", devEncoded)
		return []*api.DeviceInfo{}, err
	}
	if len(nodedevices) == 0 {
		klog.InfoS("no gpu device found", "node", n.Name, "device annotation", devEncoded)
		return []*api.DeviceInfo{}, errors.New("no gpu found on node")
	}
	devDecoded := util.EncodeNodeDevices(nodedevices)
	klog.V(5).InfoS("nodes device information", "node", n.Name, "nodedevices", devDecoded)
	return nodedevices, nil
}

func (dev *NvidiaGPUDevices) MutateAdmission(ctr *corev1.Container) bool {
	/*gpu related */
	priority, ok := ctr.Resources.Limits[corev1.ResourceName(ResourcePriority)]
	if ok {
		ctr.Env = append(ctr.Env, corev1.EnvVar{
			Name:  api.TaskPriority,
			Value: fmt.Sprint(priority.Value()),
		})
	}

	_, resourceNameOK := ctr.Resources.Limits[corev1.ResourceName(ResourceName)]
	if resourceNameOK {
		return resourceNameOK
	}

	_, resourceCoresOK := ctr.Resources.Limits[corev1.ResourceName(ResourceCores)]
	_, resourceMemOK := ctr.Resources.Limits[corev1.ResourceName(ResourceMem)]
	_, resourceMemPercentageOK := ctr.Resources.Limits[corev1.ResourceName(ResourceMemPercentage)]

	if resourceCoresOK || resourceMemOK || resourceMemPercentageOK {
		if config.DefaultResourceNum > 0 {
			ctr.Resources.Limits[corev1.ResourceName(ResourceName)] = *resource.NewQuantity(int64(config.DefaultResourceNum), resource.BinarySI)
			resourceNameOK = true
		}
	}

	return resourceNameOK
}

func checkGPUtype(annos map[string]string, cardtype string) bool {
	if inuse, ok := annos[GPUInUse]; ok {
		if !strings.Contains(inuse, ",") {
			if strings.Contains(strings.ToUpper(cardtype), strings.ToUpper(inuse)) {
				return true
			}
		} else {
			for _, val := range strings.Split(inuse, ",") {
				if strings.Contains(strings.ToUpper(cardtype), strings.ToUpper(val)) {
					return true
				}
			}
		}
		return false
	}
	if nouse, ok := annos[GPUNoUse]; ok {
		if !strings.Contains(nouse, ",") {
			if strings.Contains(strings.ToUpper(cardtype), strings.ToUpper(nouse)) {
				return false
			}
		} else {
			for _, val := range strings.Split(nouse, ",") {
				if strings.Contains(strings.ToUpper(cardtype), strings.ToUpper(val)) {
					return false
				}
			}
		}
		return true
	}
	return true
}

func assertNuma(annos map[string]string) bool {
	numabind, ok := annos[NumaBind]
	if ok {
		enforce, err := strconv.ParseBool(numabind)
		if err == nil && enforce {
			return true
		}
	}
	return false
}

func (dev *NvidiaGPUDevices) CheckType(annos map[string]string, d util.DeviceUsage, n util.ContainerDeviceRequest) (bool, bool, bool) {
	if strings.Compare(n.Type, NvidiaGPUDevice) == 0 {
		return true, checkGPUtype(annos, d.Type), assertNuma(annos)
	}
	return false, false, false
}

func (dev *NvidiaGPUDevices) CheckUUID(annos map[string]string, d util.DeviceUsage) bool {
	userUUID, ok := annos[GPUUseUUID]
	if ok {
		klog.V(5).Infof("check uuid for nvidia user uuid [%s], device id is %s", userUUID, d.ID)
		// use , symbol to connect multiple uuid
		userUUIDs := strings.Split(userUUID, ",")
		for _, uuid := range userUUIDs {
			if d.ID == uuid {
				return true
			}
		}
		return false
	}

	noUserUUID, ok := annos[GPUNoUseUUID]
	if ok {
		klog.V(5).Infof("check uuid for nvidia not user uuid [%s], device id is %s", noUserUUID, d.ID)
		// use , symbol to connect multiple uuid
		noUserUUIDs := strings.Split(noUserUUID, ",")
		for _, uuid := range noUserUUIDs {
			if d.ID == uuid {
				return false
			}
		}
		return true
	}

	return true
}

func (dev *NvidiaGPUDevices) PatchAnnotations(annoinput *map[string]string, pd util.PodDevices) map[string]string {
	devlist, ok := pd[NvidiaGPUDevice]
	if ok && len(devlist) > 0 {
		deviceStr := util.EncodePodSingleDevice(devlist)
		(*annoinput)[util.InRequestDevices[NvidiaGPUDevice]] = deviceStr
		(*annoinput)[util.SupportDevices[NvidiaGPUDevice]] = deviceStr
		klog.V(5).Infof("pod add notation key [%s], values is [%s]", util.InRequestDevices[NvidiaGPUDevice], deviceStr)
		klog.V(5).Infof("pod add notation key [%s], values is [%s]", util.SupportDevices[NvidiaGPUDevice], deviceStr)
	}
	return *annoinput
}

func (dev *NvidiaGPUDevices) GenerateResourceRequests(ctr *corev1.Container) util.ContainerDeviceRequest {
	resourceName := corev1.ResourceName(ResourceName)
	resourceMem := corev1.ResourceName(ResourceMem)
	resourceMemPercentage := corev1.ResourceName(ResourceMemPercentage)
	resourceCores := corev1.ResourceName(ResourceCores)
	v, ok := ctr.Resources.Limits[resourceName]
	if !ok {
		v, ok = ctr.Resources.Requests[resourceName]
	}
	if ok {
		if n, ok := v.AsInt64(); ok {
			memnum := 0
			mem, ok := ctr.Resources.Limits[resourceMem]
			if !ok {
				mem, ok = ctr.Resources.Requests[resourceMem]
			}
			if ok {
				memnums, ok := mem.AsInt64()
				if ok {
					memnum = int(memnums)
				}
			}
			mempnum := int32(101)
			mem, ok = ctr.Resources.Limits[resourceMemPercentage]
			if !ok {
				mem, ok = ctr.Resources.Requests[resourceMemPercentage]
			}
			if ok {
				mempnums, ok := mem.AsInt64()
				if ok {
					mempnum = int32(mempnums)
				}
			}
			if mempnum == 101 && memnum == 0 {
				if config.DefaultMem != 0 {
					memnum = int(config.DefaultMem)
				} else {
					mempnum = 100
				}
			}
			corenum := config.DefaultCores
			core, ok := ctr.Resources.Limits[resourceCores]
			if !ok {
				core, ok = ctr.Resources.Requests[resourceCores]
			}
			if ok {
				corenums, ok := core.AsInt64()
				if ok {
					corenum = int32(corenums)
				}
			}
			return util.ContainerDeviceRequest{
				Nums:             int32(n),
				Type:             NvidiaGPUDevice,
				Memreq:           int32(memnum),
				MemPercentagereq: int32(mempnum),
				Coresreq:         int32(corenum),
			}
		}
	}
	return util.ContainerDeviceRequest{}
}
