# Build requires mind-cluster submodule at ./mind-cluster (see .gitmodules).
# ARGs before the first FROM are only for use in FROM lines (Dockerfile spec).
ARG GOLANG_IMAGE=watering-ai-registry.cn-shanghai.cr.aliyuncs.com/kube-ai-hub/golang:1.25.5-bookworm
ARG BASE_IMAGE=watering-ai-registry.cn-shanghai.cr.aliyuncs.com/kube-ai-hub/ubuntu:22.04
FROM ${GOLANG_IMAGE} AS build

RUN apt-get update && apt-get install -y --no-install-recommends \
    gcc libc6-dev ca-certificates \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /build
COPY go.mod go.sum ./
COPY . .
ARG GOPROXY
ARG VERSION=unknown
ENV CGO_ENABLED=1
ENV GOPROXY=${GOPROXY}
RUN go mod download
RUN go build -trimpath -ldflags "-s -w -X github.com/Project-HAMi/ascend-device-plugin/version.version=${VERSION}" -o /out/ascend-device-plugin ./cmd/main.go

FROM ${BASE_IMAGE}
ENV DEBIAN_FRONTEND=noninteractive
ENV LD_LIBRARY_PATH=/usr/local/Ascend/driver/lib64:/usr/local/Ascend/driver/lib64/driver:/usr/local/Ascend/driver/lib64/common

RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates \
    && rm -rf /var/lib/apt/lists/*

COPY --from=build /out/ascend-device-plugin /usr/local/bin/ascend-device-plugin
RUN chmod 755 /usr/local/bin/ascend-device-plugin

ENTRYPOINT ["/usr/local/bin/ascend-device-plugin"]
