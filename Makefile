GO ?= go
VERSION ?= unknown
BUILDARGS ?= -ldflags '-s -w -X github.com/Project-HAMi/ascend-device-plugin/version.version=$(VERSION)'
IMG_NAME = projecthami/ascend-device-plugin
REGISTRY ?= watering-ai-registry.cn-shanghai.cr.aliyuncs.com/kube-ai-hub
IMG_TAG ?= $(REGISTRY)/$(IMG_NAME):$(VERSION)
BASE_IMAGE ?= watering-ai-registry.cn-shanghai.cr.aliyuncs.com/kube-ai-hub/ubuntu:22.04
GOLANG_IMAGE ?= watering-ai-registry.cn-shanghai.cr.aliyuncs.com/kube-ai-hub/golang:1.25.5-bookworm
GOPROXY ?= https://goproxy.cn,direct
PLATFORMS ?= linux/amd64,linux/arm64
DOCKER_BUILDX_OUTPUT ?= --push

all: ascend-device-plugin

tidy:
	$(GO) mod tidy

docker:
	docker build \
	--build-arg GOLANG_IMAGE=$(GOLANG_IMAGE) \
	--build-arg BASE_IMAGE=$(BASE_IMAGE) \
	--build-arg GOPROXY=$(GOPROXY) \
	--build-arg VERSION=$(VERSION) \
	-t $(IMG_NAME):$(VERSION) .

docker-buildx:
	docker buildx build \
	--platform $(PLATFORMS) \
	--build-arg GOLANG_IMAGE=$(GOLANG_IMAGE) \
	--build-arg BASE_IMAGE=$(BASE_IMAGE) \
	--build-arg GOPROXY=$(GOPROXY) \
	--build-arg VERSION=$(VERSION) \
	-f Dockerfile \
	-t $(IMG_TAG) \
	$(DOCKER_BUILDX_OUTPUT) \
	.

lint:
	$(GO) install github.com/golangci/golangci-lint/cmd/golangci-lint@v1.61.0
	golangci-lint run

ascend-device-plugin:
	$(GO) build $(BUILDARGS) -o ./ascend-device-plugin ./cmd/main.go

clean:
	rm -rf ./ascend-device-plugin

.PHONY: all clean docker docker-buildx