ARG GOLANG_IMAGE=watering-ai-registry.cn-shanghai.cr.aliyuncs.com/kube-ai-hub/golang:1.26.2-bookworm
ARG BASE_IMAGE=watering-ai-registry.cn-shanghai.cr.aliyuncs.com/kube-ai-hub/ubuntu:22.04
FROM $GOLANG_IMAGE AS build

ENV DEBIAN_FRONTEND=noninteractive
RUN apt-get update -y && apt-get install -y --no-install-recommends gcc make ca-certificates \
    && rm -rf /var/lib/apt/lists/*
ARG GOPROXY
ENV GOPATH=/go
ARG VERSION
WORKDIR /build
ADD . .
RUN go mod download github.com/Project-HAMi/HAMi
RUN go get github.com/Project-HAMi/ascend-device-plugin/internal/server
RUN go get huawei.com/npu-exporter
RUN go get huawei.com/npu-exporter/utils/logger@v0.0.0-00010101000000-000000000000
RUN make all

FROM $BASE_IMAGE
ENV LD_LIBRARY_PATH=/usr/local/Ascend/driver/lib64:/usr/local/Ascend/driver/lib64/driver:/usr/local/Ascend/driver/lib64/common
COPY --from=build /build/ascend-device-plugin /usr/local/bin/ascend-device-plugin
COPY --from=build /build/lib/hami-vnpu-core/* /usr/local/hami-vnpu-core-assets/

ENTRYPOINT ["ascend-device-plugin"]
