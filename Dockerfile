ARG XRAY_VERSION=v26.10.31

FROM --platform=$BUILDPLATFORM golang:1.27.3-alpine@sha256:d200d2dd76b03a99b52651fd1325aff7dcd35422c35c01bac56571b0507f3deb AS builder

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
FROM --platform=$BUILDPLATFORM golang:1.27.3-alpine@sha256:d200d2dd76b03a99b52651fd1325aff7dcd35422c35c01bac56571b0507f3deb AS singbox-builder

ARG TARGETOS
ARG TARGETARCH
ARG SINGBOX_VERSION=v1.13.21
ARG SINGBOX_TAGS=with_quic,with_utls,with_clash_api,with_v2ray_api
# Our fork of sing-box v1.13.14: adds a clash_api PUT /hysteria2/users endpoint so
# Hysteria2 user changes apply live, without restarting the core (no dropped sessions).
ARG SINGBOX_REPO=https://github.com/Free-Guy-IR/sing-box.git
ARG SINGBOX_BRANCH=main
ARG SINGBOX_COMMIT=dbd00be874a06d9870b2602dd92885367384c779

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
FROM --platform=$BUILDPLATFORM golang:1.27.3-alpine@sha256:d200d2dd76b03a99b52651fd1325aff7dcd35422c35c01bac56571b0507f3deb AS xray-builder

ARG TARGETOS
ARG TARGETARCH
ARG XRAY_VERSION
ARG XRAY_COMMIT=556767e40baa142641838158d6cba743c879e5c3

RUN apk update && apk add --no-cache git

WORKDIR /xray-src
COPY patches/xray-commander-message-size.patch /patches/xray-commander-message-size.patch
COPY patches/xray-dns-long-domain.patch /patches/xray-dns-long-domain.patch
COPY patches/xray-dokodemo-unix-socket.patch /patches/xray-dokodemo-unix-socket.patch
COPY patches/xray-pinned-ca-servername.patch /patches/xray-pinned-ca-servername.patch
COPY patches/xray-quic-sniff.patch /patches/xray-quic-sniff.patch
COPY patches/xray-router-race.patch /patches/xray-router-race.patch
COPY patches/xray-session-revoke.patch /patches/xray-session-revoke.patch
COPY patches/xray-ss2022-dispatch.patch /patches/xray-ss2022-dispatch.patch
COPY patches/xray-stats-register-race.patch /patches/xray-stats-register-race.patch
COPY patches/xray-trusted-xff.patch /patches/xray-trusted-xff.patch
COPY patches/xray-validator-credential-owner.patch /patches/xray-validator-credential-owner.patch
COPY patches/xray-websocket-early-data.patch /patches/xray-websocket-early-data.patch
COPY patches/xray-wireguard-client-close.patch /patches/xray-wireguard-client-close.patch
COPY patches/xray-wireguard-tun-close.patch /patches/xray-wireguard-tun-close.patch
COPY patches/xray-xff-trusted-header.patch /patches/xray-xff-trusted-header.patch
COPY patches/xray-xhttp-client-race.patch /patches/xray-xhttp-client-race.patch
COPY patches/xray-udp-conn-cancel-race.patch /patches/xray-udp-conn-cancel-race.patch
RUN git init -q . \
 && git remote add origin https://github.com/XTLS/Xray-core.git \
 && git fetch -q --depth 1 origin ${XRAY_COMMIT} \
 && git checkout -q FETCH_HEAD \
 && test "$(git rev-parse HEAD)" = "${XRAY_COMMIT}" \
 && git apply --verbose /patches/xray-commander-message-size.patch \
 && grep -q "grpc.MaxRecvMsgSize(commanderMaxMessageSize)" app/commander/commander.go \
 && ! grep -q "MaxSendMsgSize" app/commander/commander.go \
 && git apply --verbose /patches/xray-dns-long-domain.patch \
 && grep -q "name, err := dnsmessage.NewName(domain)" app/dns/dnscommon.go \
 && ! grep -q "MustNewName" app/dns/dnscommon.go \
 && git apply --verbose /patches/xray-dokodemo-unix-socket.patch \
 && grep -qF "if dest.Port == 0 && port != \"\" {" proxy/dokodemo/dokodemo.go \
 && grep -q "google.golang.org/grpc v1.85.0$" go.mod \
 && grep -q "golang.org/x/net v0.62.0$" go.mod \
 && grep -q "golang.org/x/text v0.43.0 // indirect$" go.mod \
 && grep -qF "len(ResponseHeader1xx) >= 4 && bytes.Equal" proxy/http/server.go \
 && git apply --verbose /patches/xray-pinned-ca-servername.patch \
 && grep -qF 'return errors.New("Pinning CA needs a valid ServerName")' transport/internet/tls/config.go \
 && grep -qF "config := tlsConfig.GetTLSConfig(tls.WithDestination(dest))" transport/internet/grpc/dial.go \
 && ! grep -qF "config.ServerName = address.Domain()" transport/internet/grpc/dial.go \
 && grep -qE "tlsConfig:[[:space:]]+tlsConfig.GetTLSConfig\(tls.WithDestination\(dest\)\)," transport/internet/hysteria/dialer.go \
 && git apply --verbose /patches/xray-quic-sniff.patch \
 && grep -q "func readShortQUICVarint" common/protocol/quic/sniff.go \
 && grep -qF "len(b) < hdrLen+4+block.BlockSize()" common/protocol/quic/sniff.go \
 && git apply --verbose /patches/xray-router-race.patch \
 && grep -qF "rules          atomic.Pointer[[]*Rule]" app/router/router.go \
 && grep -qF "balancers      atomic.Pointer[map[string]*Balancer]" app/router/router.go \
 && test "$(grep -cF 'rules := *r.rules.Load()' app/router/router.go)" = "2" \
 && grep -qF "r.balancers.Store(&newBalancers)" app/router/router.go \
 && grep -qF "closeWebhooks(removed)" app/router/router.go \
 && test "$(grep -cF '(*r.balancers.Load())[tag]' app/router/balancing.go)" = "3" \
 && ! (grep -hE "(^|[^[:alnum:]_])r[.](rules|balancers)" app/router/router.go app/router/balancing.go app/router/balancing_override.go | grep -qvE "r[.](rules|balancers)[.](Load|Store)[(]") \
 && test -f app/router/router_writers_race_test.go \
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
 && test ! -d common/singbridge \
 && ! grep -rqF "sagernet/sing-shadowsocks" proxy/shadowsocks_2022 \
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
 && git apply --verbose /patches/xray-validator-credential-owner.patch \
 && grep -qF "held, loaded := v.users.LoadOrStore(key, u)" proxy/vless/validator.go \
 && grep -qF "v.users.CompareAndDelete(ProcessUUID(u.(*protocol.MemoryUser).Account.(*MemoryAccount).ID.UUID()), u)" proxy/vless/validator.go \
 && grep -qF "v.email.CompareAndDelete(le, u)" proxy/vless/validator.go \
 && ! grep -qF "v.users.Store(" proxy/vless/validator.go \
 && ! grep -qF "v.users.Delete(" proxy/vless/validator.go \
 && grep -qF "held, loaded := v.users.LoadOrStore(key, u)" proxy/trojan/validator.go \
 && grep -qF "v.users.CompareAndDelete(hexString(u.(*protocol.MemoryUser).Account.(*MemoryAccount).Key), u)" proxy/trojan/validator.go \
 && grep -qF "v.email.CompareAndDelete(le, u)" proxy/trojan/validator.go \
 && ! grep -qF "v.users.Store(" proxy/trojan/validator.go \
 && ! grep -qF "v.users.Delete(" proxy/trojan/validator.go \
 && grep -qF "if holder, held := v.aeadDecoderHolder.Holder(key).(*protocol.MemoryUser); held && !strings.EqualFold(holder.Email, u.Email) {" proxy/vmess/validator.go \
 && grep -qF "v.aeadDecoderHolder.RemoveUserHeldBy(cmdkeyfl, u)" proxy/vmess/validator.go \
 && ! grep -qF "v.aeadDecoderHolder.RemoveUser(cmdkeyfl)" proxy/vmess/validator.go \
 && grep -qF "if item, found := a.decoders[string(key[:])]; found && item.ticket == ticket {" proxy/vmess/aead/authid.go \
 && grep -qF "h.usersByEmail.removeHeldBy(user)" proxy/vmess/inbound/inbound.go \
 && git apply --verbose /patches/xray-websocket-early-data.patch \
 && grep -qF "func (d *delayDialConn) LocalAddr() net.Addr {" transport/internet/websocket/dialer.go \
 && grep -qF "func (d *delayDialConn) RemoteAddr() net.Addr {" transport/internet/websocket/dialer.go \
 && git apply --verbose /patches/xray-wireguard-client-close.patch \
 && grep -qF "if h.dev == nil {" proxy/wireguard/client.go \
 && grep -qF "if b.PacketConn != nil {" proxy/wireguard/bind.go \
 && git apply --verbose /patches/xray-wireguard-tun-close.patch \
 && grep -qF "close(tun.closed)" proxy/wireguard/netstack.go \
 && grep -qF "tun.closeOnce.Do(func() {" proxy/wireguard/netstack.go \
 && ! grep -qF "close(tun.incomingPacket)" proxy/wireguard/netstack.go \
 && grep -q "golang.org/x/crypto v0.58.0$" go.mod \
 && grep -q "golang.org/x/net v0.62.0$" go.mod \
 && git apply --verbose /patches/xray-xff-trusted-header.patch \
 && grep -q "func TrustedClientAddress" common/protocol/http/trusted_forwarder.go \
 && grep -q '"xray.trusted.forwarder.cidrs"' common/protocol/http/trusted_forwarder.go \
 && grep -q "if !peerIsTrustedForwarder(remoteAddr) {" common/protocol/http/trusted_forwarder.go \
 && grep -qF "if prefix.Addr().Is4In6() {" common/protocol/http/trusted_forwarder.go \
 && grep -q "return TrustedClientAddress(header.Values, trusted, remoteAddr)" common/protocol/http/headers.go \
 && grep -q "return http_proto.TrustedClientAddress(md.Get, trusted, remoteAddr)" transport/internet/grpc/encoding/remoteaddr.go \
 && ! grep -q "IndexByte(value, ',')" common/protocol/http/headers.go transport/internet/grpc/encoding/remoteaddr.go \
 && git apply --verbose /patches/xray-xhttp-client-race.patch \
 && test -f transport/internet/splithttp/client_race_test.go \
 && grep -qF "reader atomic.Pointer[io.ReadCloser]" transport/internet/splithttp/client.go \
 && test "$(grep -cF 'if p := w.reader.Swap(nil); p != nil {' transport/internet/splithttp/client.go)" = "2" \
 && grep -A1 -F "w.reader.Store(&rc)" transport/internet/splithttp/client.go | grep -qF "if w.wait.Done() {" \
 && grep -A1 -F "func (w *WaitReadCloser) Close() error {" transport/internet/splithttp/client.go | grep -qF "w.wait.Close()" \
 && grep -qF "return c.closed.Load()" transport/internet/splithttp/client.go \
 && test "$(grep -c 'c.closed.Store(true)' transport/internet/splithttp/client.go)" = "3" \
 && ! grep -qE "c[.]closed = true|close[(]w[.]Wait[)]|w[.]ReadCloser" transport/internet/splithttp/client.go \
 && grep -A1 -F "n := int(buff.Len())" transport/internet/splithttp/dialer.go | grep -qF "err := w.WriteMultiBuffer(buf.MultiBuffer{buff})" \
 && grep -qF "writed += n" transport/internet/splithttp/dialer.go \
 && ! grep -qF "writed += int(buff.Len())" transport/internet/splithttp/dialer.go \
 && git apply --verbose /patches/xray-udp-conn-cancel-race.patch \
 && grep -qF "w.publishCancel(conn, cancel)" app/proxyman/inbound/worker.go \
 && grep -qF "func (w *udpWorker) publishCancel(conn *udpConn, cancel context.CancelFunc) {" app/proxyman/inbound/worker.go \
 && ! grep -qE "^[[:space:]]{3}conn\.cancel = cancel$" app/proxyman/inbound/worker.go \
 && grep -qF "func TestUDPConnCancelIsPublishedUnderTheWorkerLockBeforeCleanClosesIt" app/proxyman/inbound/udp_conn_cancel_race_test.go
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -trimpath \
    -ldflags "-s -w -buildid=" \
    -o /out/xray ./main

FROM --platform=$BUILDPLATFORM alpine:3.24.2@sha256:294b683cb724975bec92580e1e685676bd4b50bda910ddb8c51d4cabeaec77e6 AS geo-assets

ARG GEOFILES_TAG=202610202206
ARG GEOIP_SHA256=4a1b1eddabf5614fa51dd93ac0498856cb82dd65933cd6d6d5890d8c7bb5e902
ARG GEOSITE_SHA256=9a4f9f84cf6201cf135afbc9d67190359cdc28e4e02e2e9f3e2b359cac317ed2

RUN apk add --no-cache curl
WORKDIR /geo
RUN curl -fsSL --retry 3 -o /geo/geoip.dat "https://github.com/Loyalsoldier/v2ray-rules-dat/releases/download/${GEOFILES_TAG}/geoip.dat" \
 && curl -fsSL --retry 3 -o /geo/geosite.dat "https://github.com/Loyalsoldier/v2ray-rules-dat/releases/download/${GEOFILES_TAG}/geosite.dat" \
 && echo "${GEOIP_SHA256}  /geo/geoip.dat" | sha256sum -c - \
 && echo "${GEOSITE_SHA256}  /geo/geosite.dat" | sha256sum -c - \
 && chmod 0644 /geo/geoip.dat /geo/geosite.dat

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
