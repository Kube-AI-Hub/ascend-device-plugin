# Ascend Device Plugin
[![FOSSA Status](https://app.fossa.com/api/projects/git%2Bgithub.com%2FProject-HAMi%2Fascend-device-plugin.svg?type=shield)](https://app.fossa.com/projects/git%2Bgithub.com%2FProject-HAMi%2Fascend-device-plugin?ref=badge_shield)


## Introduction

This Ascend device plugin is implemented for [HAMi](https://github.com/Project-HAMi/HAMi) and [volcano](https://github.com/volcano-sh/volcano) scheduling.

Memory slicing is supported based on virtualization template, lease available template is automatically used. For detailed information, check [template](./ascend-device-configmap.yaml)

## Prerequisites

[ascend-docker-runtime](https://gitcode.com/Ascend/mind-cluster/tree/master/component/ascend-docker-runtime)

```bash
git submodule add https://gitcode.com/Ascend/mind-cluster.git
```

If `mind-cluster` is not at `./mind-cluster` (for example it sits next to this repo as `../mind-cluster`), either run `git submodule update --init` so `./mind-cluster` exists, or create a symlink before `make` / `docker build`:

```bash
ln -snf ../mind-cluster mind-cluster
```

## Compile

```bash
make all
```

Flags include `--check_idle_vnpu_interval` (seconds, default 60, range 3–1800) and `--hami_register_interval` (seconds, default 30, range 3–1800) for periodic HAMi node registration updates.

### Build

Single-arch local image:

```bash
docker build -t $IMAGE_NAME .
```

Multi-arch registry image (same pattern as HAMi `build.sh`; requires `docker buildx` and `docker login` to the registry when pushing):

```bash
./build.sh
```

Override registry, image name, platforms, or skip push (load locally):

```bash
REGISTRY=watering-ai-registry.cn-shanghai.cr.aliyuncs.com/kube-ai-hub \
  IMG_NAME=ascend-device-plugin \
  PLATFORMS=linux/amd64,linux/arm64 \
  ./build.sh

PLATFORMS=linux/amd64 DOCKER_PUSH=0 ./build.sh
```

Image tag is read from `image-version.txt` (or set `VERSION=...`).

## Deployment

### Label the Node with `ascend=on`


```
kubectl label node {ascend-node} ascend=on
``` 

### Deploy ConfigMap

```
kubectl apply -f ascend-device-configmap.yaml
```

### Deploy `ascend-device-plugin`

```bash
kubectl apply -f ascend-device-plugin.yaml
```

If scheduling Ascend devices in HAMi, simply set `devices.ascend.enabled` to true when deploying HAMi, and the ConfigMap and `ascend-device-plugin` will be automatically deployed. refer https://github.com/Project-HAMi/HAMi/blob/master/charts/hami/README.md#huawei-ascend

## Usage

To exclusively use an entire card or request multiple cards, you only need to set the corresponding resourceName. If multiple tasks need to share the same NPU, you need to set the corresponding resource request to 1 and configure the appropriate ResourceMemoryName.

### Usage in HAMi

```yaml
...
    containers:
    - name: npu_pod
      ...
      resources:
        limits:
          huawei.com/Ascend910B: "1"
          # if you don't specify Ascend910B-memory, it will use a whole NPU.
          huawei.com/Ascend910B-memory: "4096"
```

For more examples, see [examples](./examples/)

### Usage in volcano

Volcano must be installed prior to usage, for more information see [here](https://github.com/volcano-sh/volcano/tree/master/docs/user-guide/how_to_use_vnpu.md)

```yaml
apiVersion: v1
kind: Pod
metadata:
  name: ascend-pod
spec:
  schedulerName: volcano
  containers:
    - name: ubuntu-container
      image: swr.cn-south-1.myhuaweicloud.com/ascendhub/ascend-pytorch:24.0.RC1-A2-1.11.0-ubuntu20.04
      command: ["sleep"]
      args: ["100000"]
      resources:
        limits:
          huawei.com/Ascend310P: "1"
           huawei.com/Ascend310P-memory: "4096"
 ```

## License
[![FOSSA Status](https://app.fossa.com/api/projects/git%2Bgithub.com%2FProject-HAMi%2Fascend-device-plugin.svg?type=large)](https://app.fossa.com/projects/git%2Bgithub.com%2FProject-HAMi%2Fascend-device-plugin?ref=badge_large)