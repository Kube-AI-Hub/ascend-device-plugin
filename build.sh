#!/bin/bash
set -eu -o pipefail

# Multi-arch image build via docker buildx. Requires: docker buildx, and for --push:
#   docker login watering-ai-registry.cn-shanghai.cr.aliyuncs.com
# Override: PLATFORMS=... REGISTRY=... IMG_NAME=... VERSION=... DOCKER_PUSH=0
# Single platform local load: PLATFORMS=linux/amd64 DOCKER_PUSH=0 ./build.sh

REGISTRY="${REGISTRY:-watering-ai-registry.cn-shanghai.cr.aliyuncs.com/kube-ai-hub}"
IMG_NAME="${IMG_NAME:-ascend-device-plugin}"
PLATFORMS="${PLATFORMS:-linux/amd64,linux/arm64}"
BUILDER_NAME="${BUILDER_NAME:-ascend-device-plugin-builder}"

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
if [ -n "${VERSION:-}" ]; then
    :
elif [ -f "${ROOT_DIR}/image-version.txt" ]; then
    VERSION="$(tr -d ' \t\n' < "${ROOT_DIR}/image-version.txt")"
else
    VERSION="v1.2.1"
fi

IMG_TAG="${REGISTRY}/${IMG_NAME}:${VERSION}"

DOCKER_BUILDX_OUTPUT="--push"
if [[ "${PLATFORMS}" != *","* ]]; then
    if [[ "${DOCKER_PUSH:-1}" != "1" ]]; then
        DOCKER_BUILDX_OUTPUT="--load"
    fi
fi

TARGET_ARCH="${TARGET_ARCH:-amd64}"
if [[ "${PLATFORMS}" != *","* ]]; then
    case "${PLATFORMS}" in
        linux/arm64) TARGET_ARCH=arm64 ;;
        linux/amd64) TARGET_ARCH=amd64 ;;
    esac
fi

echo "Building ascend-device-plugin image: ${IMG_TAG}"
echo "Platforms: ${PLATFORMS}"
echo "Version: ${VERSION}"

if ! docker buildx version &>/dev/null; then
    echo "Error: docker buildx is required"
    exit 1
fi

if ! docker buildx inspect "${BUILDER_NAME}" &>/dev/null; then
    echo "Creating buildx builder: ${BUILDER_NAME}"
    docker buildx create --name "${BUILDER_NAME}" --use
else
    docker buildx use "${BUILDER_NAME}"
fi

docker buildx inspect --bootstrap

cd "${ROOT_DIR}"

make docker-buildx \
    IMG_TAG="${IMG_TAG}" \
    IMG_NAME="${IMG_NAME}" \
    REGISTRY="${REGISTRY}" \
    VERSION="${VERSION}" \
    TARGET_ARCH="${TARGET_ARCH}" \
    PLATFORMS="${PLATFORMS}" \
    DOCKER_BUILDX_OUTPUT="${DOCKER_BUILDX_OUTPUT}" \
    GOLANG_IMAGE="${GOLANG_IMAGE:-watering-ai-registry.cn-shanghai.cr.aliyuncs.com/kube-ai-hub/golang:1.25.5-bookworm}" \
    BASE_IMAGE="${BASE_IMAGE:-watering-ai-registry.cn-shanghai.cr.aliyuncs.com/kube-ai-hub/ubuntu:22.04}" \
    GOPROXY="${GOPROXY:-https://goproxy.cn,direct}"

if [[ "${DOCKER_BUILDX_OUTPUT}" == "--push" ]]; then
    echo "Successfully built and pushed: ${IMG_TAG}"
else
    echo "Successfully built (loaded locally): ${IMG_TAG}"
fi
