FROM --platform=$BUILDPLATFORM golang:1.26.3-alpine AS builder

ARG TARGETOS
ARG TARGETARCH

RUN apk update && apk add --no-cache make

WORKDIR /src

COPY go* .
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} make NAME=main build
RUN GOOS=${TARGETOS} GOARCH=${TARGETARCH} make install_xray

# --- sing-box (Hysteria2) builder ---
# Built from source rather than using the official prebuilt sing-box release binary:
# official releases are not compiled with the "with_v2ray_api" tag, which this node's
# backend/singbox stats client (v2ray-api gRPC, see backend/singbox and config.go's
# SINGBOX_EXECUTABLE_PATH) requires to report per-user traffic. Version and tag set are
# pinned to match what was built and traffic-tested (incl. Salamander-obfuscated
# Hysteria2 against real QUIC-blocking ISPs) for this fork.
FROM --platform=$BUILDPLATFORM golang:1.26.3-alpine AS singbox-builder

ARG TARGETOS
ARG TARGETARCH
ARG SINGBOX_VERSION=v1.13.14
ARG SINGBOX_TAGS=with_quic,with_utls,with_clash_api,with_v2ray_api

RUN apk update && apk add --no-cache git

WORKDIR /singbox-src
RUN git clone --depth 1 --branch ${SINGBOX_VERSION} https://github.com/SagerNet/sing-box.git .
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -trimpath \
    -tags "${SINGBOX_TAGS}" \
    -ldflags "-X 'github.com/sagernet/sing-box/constant.Version=${SINGBOX_VERSION}' -s -w -buildid=" \
    -o /out/sing-box ./cmd/sing-box

FROM alpine:latest

LABEL org.opencontainers.image.source="https://github.com/Free-Guy-IR/node"

RUN apk update && apk add --no-cache wireguard-tools nftables iproute2 procps iptables openvpn

WORKDIR /app
COPY --from=builder /src/main /app/main
COPY --from=builder /usr/local/bin/xray /usr/local/bin/xray
COPY --from=builder /usr/local/share/xray /usr/local/share/xray
COPY --from=singbox-builder /out/sing-box /usr/local/bin/sing-box

ENTRYPOINT ["./main"]
