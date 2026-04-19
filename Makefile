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

# Local-directory buildx cache. No network roundtrip, survives builder
# recreation / BuildKit GC, and is shared across all docker-container builders.
# Override BUILD_CACHE_DIR to relocate, or set BUILD_CACHE= to disable.
# Periodically clean with: make container-cache-prune
BUILD_CACHE_DIR ?= $(HOME)/.cache/buildx/ascend-device-plugin
BUILD_CACHE ?= --cache-to type=local,dest=$(BUILD_CACHE_DIR),mode=max,compression=zstd,compression-level=3 --cache-from type=local,src=$(BUILD_CACHE_DIR)

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
	@mkdir -p $(BUILD_CACHE_DIR)
	docker buildx build \
	--platform $(PLATFORMS) \
	$(BUILD_CACHE) \
	--build-arg GOLANG_IMAGE=$(GOLANG_IMAGE) \
	--build-arg BASE_IMAGE=$(BASE_IMAGE) \
	--build-arg GOPROXY=$(GOPROXY) \
	--build-arg VERSION=$(VERSION) \
	-f Dockerfile \
	-t $(IMG_TAG) \
	$(DOCKER_BUILDX_OUTPUT) \
	.

container-cache-prune:
	@echo "Removing local buildx cache at $(BUILD_CACHE_DIR)..."
	rm -rf $(BUILD_CACHE_DIR)

lint:
	$(GO) install github.com/golangci/golangci-lint/cmd/golangci-lint@v1.61.0
	golangci-lint run

ascend-device-plugin:
	$(GO) build $(BUILDARGS) -o ./ascend-device-plugin ./cmd/main.go

clean:
	rm -rf ./ascend-device-plugin

.PHONY: all clean docker docker-buildx container-cache-prune