ARG XRAY_VERSION=v26.3.27

FROM --platform=$BUILDPLATFORM golang:1.26.6-alpine@sha256:3889b425f035be855a72fb4755265311293b6d414521f0a519d819df32222d83 AS builder

ARG TARGETOS
ARG TARGETARCH

RUN apk update && apk add --no-cache make git

WORKDIR /src

COPY go* .
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} make NAME=main build

# --- sing-box (Hysteria2) builder ---
# Built from source rather than using the official prebuilt sing-box release binary:
# official releases are not compiled with the "with_v2ray_api" tag, which this node's
# backend/singbox stats client (v2ray-api gRPC, see backend/singbox and config.go's
# SINGBOX_EXECUTABLE_PATH) requires to report per-user traffic. Version and tag set are
# pinned to match what was built and traffic-tested (incl. Salamander-obfuscated
# Hysteria2 against real QUIC-blocking ISPs) for this fork.
FROM --platform=$BUILDPLATFORM golang:1.26.6-alpine@sha256:3889b425f035be855a72fb4755265311293b6d414521f0a519d819df32222d83 AS singbox-builder

ARG TARGETOS
ARG TARGETARCH
ARG SINGBOX_VERSION=v1.13.14
ARG SINGBOX_TAGS=with_quic,with_utls,with_clash_api,with_v2ray_api
# Our fork of sing-box v1.13.14: adds a clash_api PUT /hysteria2/users endpoint so
# Hysteria2 user changes apply live, without restarting the core (no dropped sessions).
ARG SINGBOX_REPO=https://github.com/Free-Guy-IR/sing-box.git
ARG SINGBOX_BRANCH=v1.13.14-hotreload
ARG SINGBOX_COMMIT=4a87f503d6c1279a81c8c0ec7eb6a8ff1c7af44c

RUN apk update && apk add --no-cache git

WORKDIR /singbox-src
RUN git init -q . \
 && git remote add origin ${SINGBOX_REPO} \
 && git fetch -q --depth 1 origin ${SINGBOX_COMMIT} \
 && git checkout -q FETCH_HEAD \
 && test "$(git rev-parse HEAD)" = "${SINGBOX_COMMIT}"
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -trimpath \
    -tags "${SINGBOX_TAGS}" \
    -ldflags "-X 'github.com/sagernet/sing-box/constant.Version=${SINGBOX_VERSION}' -s -w -buildid=" \
    -o /out/sing-box ./cmd/sing-box

# --- xray builder ---
# Built from source rather than using the prebuilt release binary installed by
# make install_xray: Xray's commander starts its gRPC server with no options, so it
# inherits grpc-go's 4 MiB receive default, and a routing rule that names a large
# geosite category is expanded past that before it reaches the core. The patch gives
# that server the same 64 MiB this node already allows elsewhere. The build applies the
# patch and then greps for the result, so a patch that stops applying fails the build
# instead of silently shipping an unpatched core. The source is pinned to a commit and
# the checkout is asserted against it, the same way the sing-box stage does, because a
# git tag can be moved and a tag alone would not tell us we built what we reviewed.
FROM --platform=$BUILDPLATFORM golang:1.26.6-alpine@sha256:3889b425f035be855a72fb4755265311293b6d414521f0a519d819df32222d83 AS xray-builder

ARG TARGETOS
ARG TARGETARCH
ARG XRAY_VERSION
ARG XRAY_COMMIT=d2758a023cd7f4174a5a5fa4ff66e487d4342ba0

RUN apk update && apk add --no-cache git

WORKDIR /xray-src
COPY patches/xray-commander-message-size.patch /patches/xray-commander-message-size.patch
COPY patches/xray-dns-long-domain.patch /patches/xray-dns-long-domain.patch
COPY patches/xray-dokodemo-unix-socket.patch /patches/xray-dokodemo-unix-socket.patch
COPY patches/xray-grpc-1.83.2.patch /patches/xray-grpc-1.83.2.patch
COPY patches/xray-http-100-continue.patch /patches/xray-http-100-continue.patch
COPY patches/xray-quic-sniff.patch /patches/xray-quic-sniff.patch
COPY patches/xray-router-race.patch /patches/xray-router-race.patch
COPY patches/xray-session-revoke.patch /patches/xray-session-revoke.patch
COPY patches/xray-ss2022-dispatch.patch /patches/xray-ss2022-dispatch.patch
COPY patches/xray-stats-register-race.patch /patches/xray-stats-register-race.patch
COPY patches/xray-trusted-xff.patch /patches/xray-trusted-xff.patch
COPY patches/xray-websocket-early-data.patch /patches/xray-websocket-early-data.patch
COPY patches/xray-wireguard-client-close.patch /patches/xray-wireguard-client-close.patch
COPY patches/xray-wireguard-tun-close.patch /patches/xray-wireguard-tun-close.patch
COPY patches/xray-x-net-0.59.0.patch /patches/xray-x-net-0.59.0.patch
COPY patches/xray-xff-trusted-header.patch /patches/xray-xff-trusted-header.patch
RUN git init -q . \
 && git remote add origin https://github.com/XTLS/Xray-core.git \
 && git fetch -q --depth 1 origin ${XRAY_COMMIT} \
 && git checkout -q FETCH_HEAD \
 && test "$(git rev-parse HEAD)" = "${XRAY_COMMIT}" \
 && git apply --verbose /patches/xray-commander-message-size.patch \
 && grep -q "grpc.MaxRecvMsgSize(commanderMaxMessageSize)" app/commander/commander.go \
 && git apply --verbose /patches/xray-dns-long-domain.patch \
 && grep -q "name, err := dnsmessage.NewName(domain)" app/dns/dnscommon.go \
 && ! grep -q "MustNewName" app/dns/dnscommon.go \
 && git apply --verbose /patches/xray-dokodemo-unix-socket.patch \
 && grep -qF "if dest.Port == 0 && port != \"\" {" proxy/dokodemo/dokodemo.go \
 && git apply --verbose /patches/xray-grpc-1.83.2.patch \
 && grep -q "google.golang.org/grpc v1.83.2$" go.mod \
 && grep -q "golang.org/x/net v0.58.0$" go.mod \
 && grep -q "golang.org/x/text v0.41.0 // indirect$" go.mod \
 && git apply --verbose /patches/xray-http-100-continue.patch \
 && grep -qF "len(ResponseHeader1xx) >= 4 && bytes.Equal" proxy/http/server.go \
 && git apply --verbose /patches/xray-quic-sniff.patch \
 && grep -q "func readShortQuicVarint" common/protocol/quic/sniff.go \
 && grep -qF "len(b) < hdrLen+4+block.BlockSize()" common/protocol/quic/sniff.go \
 && git apply --verbose /patches/xray-router-race.patch \
 && grep -qF "table          atomic.Pointer[routeTable]" app/router/router.go \
 && grep -qF "rules := r.snapshot().rules" app/router/router.go \
 && grep -qF "next, err := r.extend(base, config)" app/router/router.go \
 && grep -qF "closeRuleWebhooks(removed)" app/router/router.go \
 && grep -qF "func (r *Router) balancer(tag string) (*Balancer, bool) {" app/router/router.go \
 && ! grep -qE "(^|[^[:alnum:]_])r[.](rules|balancers)([^[:alnum:]_]|$)" app/router/router.go app/router/balancing.go app/router/balancing_override.go \
 && grep -qF "b, ok := r.balancer(balancer)" app/router/balancing_override.go \
 && git apply --verbose /patches/xray-session-revoke.patch \
 && test "$(grep -c 'revocable.Wrap(w.dispatcher)' app/proxyman/inbound/worker.go)" = "3" \
 && test "$(grep -c 'revocable.Release()' app/proxyman/inbound/worker.go)" = "3" \
 && grep -qF "w.proxy.Process(ctx, net.Network_UDP, conn, revocable.Wrap(w.dispatcher))" app/proxyman/inbound/worker.go \
 && grep -qF "if w.activeConn[id] == conn {" app/proxyman/inbound/worker.go \
 && grep -qF "w.removeConn(id, conn)" app/proxyman/inbound/worker.go \
 && grep -qF "for ctx.Err() == nil {" proxy/shadowsocks/server.go \
 && grep -qF "buf.ReleaseMulti(mpayload[i:])" proxy/shadowsocks/server.go \
 && grep -q "retireUser(handler.Tag(), op.Email, um.GetUser(ctx, op.Email))" app/proxyman/command/command.go \
 && grep -q "RevokeUserOperation" app/proxyman/sessionrevoke/config.pb.go \
 && grep -qF "message RevokeUserOperation {" app/proxyman/sessionrevoke/config.proto \
 && grep -qF "func (op *RevokeUserOperation) ApplyInbound(ctx context.Context, handler inbound.Handler) error {" app/proxyman/sessionrevoke/operation.go \
 && grep -qF "if err := manager.RemoveUser(ctx, op.Email); err != nil {" app/proxyman/sessionrevoke/operation.go \
 && grep -qF "closed := Revoke(handler.Tag(), op.Email, current)" app/proxyman/sessionrevoke/operation.go \
 && grep -qF "released bool" app/proxyman/sessionrevoke/session.go \
 && grep -qF "s.released = true" app/proxyman/sessionrevoke/session.go \
 && grep -qF "if s.released {" app/proxyman/sessionrevoke/session.go \
 && git apply --verbose /patches/xray-ss2022-dispatch.patch \
 && grep -qF "func ToDestination(socksaddr M.Socksaddr, network net.Network) (net.Destination, error)" common/singbridge/destination.go \
 && grep -q "destination, err := singbridge.ToDestination(metadata.Destination, net.Network_UDP)" proxy/shadowsocks_2022/inbound_relay.go \
 && grep -qF "conn.T = signal.CancelAfterInactivity(ctx, cancel, 300*time.Second)" common/singbridge/pipe.go \
 && ! grep -qF "time.After(300 * time.Second)" common/singbridge/pipe.go \
 && grep -qF "T: signal.CancelAfterInactivity(ctx, func() {" proxy/shadowsocks_2022/outbound.go \
 && git apply --verbose /patches/xray-stats-register-race.patch \
 && grep -qF "func (m *Manager) GetOrRegisterCounter(name string) (stats.Counter, error) {" app/stats/stats.go \
 && grep -qF "func (m *Manager) GetOrRegisterOnlineMap(name string) (stats.OnlineMap, error) {" app/stats/stats.go \
 && ! grep -rqE "(^|[^.[:alnum:]_])stats[.]GetOrRegister(Counter|OnlineMap|Channel)[(]" app proxy features \
 && git apply --verbose /patches/xray-trusted-xff.patch \
 && grep -q "func ApplyTrustedXForwardedFor" common/protocol/http/headers.go \
 && grep -q "http_proto.ApplyTrustedXForwardedFor" transport/internet/websocket/hub.go \
 && grep -q "http_proto.ApplyTrustedXForwardedFor" transport/internet/httpupgrade/hub.go \
 && grep -q "http_proto.ApplyTrustedXForwardedFor" transport/internet/splithttp/hub.go \
 && grep -q "encoding.NewHunkConn(server, cancel, l.trustedXForwardedFor)" transport/internet/grpc/hub.go \
 && ! grep -rq "http_proto.ParseXForwardedFor" transport \
 && ! grep -rq "x-real-ip" transport/internet/grpc \
 && git apply --verbose /patches/xray-websocket-early-data.patch \
 && grep -qF "func (d *delayDialConn) LocalAddr() net.Addr {" transport/internet/websocket/dialer.go \
 && grep -qF "func (d *delayDialConn) RemoteAddr() net.Addr {" transport/internet/websocket/dialer.go \
 && git apply --verbose /patches/xray-wireguard-client-close.patch \
 && grep -qF "tunnel, err := h.processWireGuard(ctx, dialer)" proxy/wireguard/client.go \
 && grep -qF "if h.closed {" proxy/wireguard/client.go \
 && ! grep -qE "h[.]net[.]Dial" proxy/wireguard/client.go \
 && test "$(grep -c 'case <-bind.closedCh:' proxy/wireguard/bind.go)" = "3" \
 && grep -B1 -F "close(bind.closedCh)" proxy/wireguard/bind.go | grep -qF "default:" \
 && git apply --verbose /patches/xray-wireguard-tun-close.patch \
 && grep -qF "close(tun.closed)" proxy/wireguard/gvisortun/tun.go \
 && ! grep -qF "close(tun.incomingPacket)" proxy/wireguard/gvisortun/tun.go \
 && git apply --verbose /patches/xray-x-net-0.59.0.patch \
 && grep -q "golang.org/x/crypto v0.57.0$" go.mod \
 && grep -q "golang.org/x/net v0.59.0$" go.mod \
 && git apply --verbose /patches/xray-xff-trusted-header.patch \
 && grep -q "func TrustedClientAddress" common/protocol/http/trusted_forwarder.go \
 && grep -q '"xray.trusted.forwarder.cidrs"' common/protocol/http/trusted_forwarder.go \
 && grep -q "if !peerIsTrustedForwarder(remoteAddr) {" common/protocol/http/trusted_forwarder.go \
 && grep -qF "if prefix.Addr().Is4In6() {" common/protocol/http/trusted_forwarder.go \
 && grep -q "return TrustedClientAddress(header.Values, trusted, remoteAddr)" common/protocol/http/headers.go \
 && grep -q "return http_proto.TrustedClientAddress(md.Get, trusted, remoteAddr)" transport/internet/grpc/encoding/remoteaddr.go \
 && ! grep -q "IndexByte(value, ',')" common/protocol/http/headers.go transport/internet/grpc/encoding/remoteaddr.go
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -trimpath \
    -ldflags "-s -w -buildid=" \
    -o /out/xray ./main

FROM --platform=$BUILDPLATFORM alpine:3.24.2@sha256:294b683cb724975bec92580e1e685676bd4b50bda910ddb8c51d4cabeaec77e6 AS geo-assets

ARG XRAY_VERSION
ARG XRAY_ZIP_SHA256=23cd9af937744d97776ee35ecad4972cf4b2109d1e0fe6be9930467608f7c8ae
ARG GEOIP_SHA256=744c97b74c52bae2ac8664fef6ac481d7765cb8432a0df54f0368a88b9b4a354
ARG GEOSITE_SHA256=adf92de0cfc70e458b399f04c5f912bf42d115ed7e37281b30e2f1c68605e4e9

RUN apk add --no-cache curl
WORKDIR /geo
RUN curl -fsSL --retry 3 -o /tmp/xray.zip "https://github.com/XTLS/Xray-core/releases/download/${XRAY_VERSION}/Xray-linux-64.zip" \
 && echo "${XRAY_ZIP_SHA256}  /tmp/xray.zip" | sha256sum -c - \
 && unzip -q /tmp/xray.zip geoip.dat geosite.dat -d /geo \
 && echo "${GEOIP_SHA256}  /geo/geoip.dat" | sha256sum -c - \
 && echo "${GEOSITE_SHA256}  /geo/geosite.dat" | sha256sum -c - \
 && chmod 0644 /geo/geoip.dat /geo/geosite.dat \
 && rm /tmp/xray.zip

FROM alpine:3.24.2@sha256:294b683cb724975bec92580e1e685676bd4b50bda910ddb8c51d4cabeaec77e6 AS filter-assets

ARG SKIP_FILTER_ASSETS=0

RUN apk add --no-cache python3 ca-certificates
WORKDIR /assets
COPY scripts/build_filter_assets.py /build_filter_assets.py
RUN if [ "$SKIP_FILTER_ASSETS" = "1" ]; then \
        echo "filter assets skipped"; \
        : > /assets/pgfilter.dat; \
        echo '{"file":"pgfilter.dat","sha256":"","lists":[]}' > /assets/pgfilter_index.json; \
    else \
        python3 /build_filter_assets.py /assets; \
    fi

FROM alpine:3.24.2@sha256:294b683cb724975bec92580e1e685676bd4b50bda910ddb8c51d4cabeaec77e6

LABEL org.opencontainers.image.source="https://github.com/Free-Guy-IR/node"

RUN apk update && apk add --no-cache wireguard-tools nftables iproute2 procps iptables openvpn strongswan xl2tpd ppp
RUN command -v swanctl >/dev/null && command -v xl2tpd >/dev/null && command -v pppd >/dev/null && command -v nft >/dev/null \
    && { test -x /usr/lib/strongswan/charon || test -x /usr/lib/ipsec/charon || test -x /usr/libexec/ipsec/charon; } \
    && test -f /etc/swanctl/swanctl.conf

WORKDIR /app
COPY --from=builder /src/main /app/main
COPY --from=xray-builder /out/xray /usr/local/bin/xray
COPY --from=geo-assets /geo/geoip.dat /usr/local/share/xray/geoip.dat
COPY --from=geo-assets /geo/geosite.dat /usr/local/share/xray/geosite.dat
COPY --from=singbox-builder /out/sing-box /usr/local/bin/sing-box
COPY --from=filter-assets /assets/pgfilter.dat /usr/local/share/xray/pgfilter.dat
COPY --from=filter-assets /assets/pgfilter_index.json /usr/local/share/xray/pgfilter_index.json

ENV XRAY_LOCATION_ASSET=/usr/local/share/xray

ENTRYPOINT ["./main"]
