NAME = pasarguard-node-$(GOOS)-$(GOARCH)

LDFLAGS = -s -w -buildid=
PARAMS = -trimpath -ldflags "$(LDFLAGS)" -v
MAIN = ./cmd/node
PREFIX ?= $(shell go env GOPATH)
XRAY_OS ?=
XRAY_ARCH ?=
# Map GOARCH to installer arch flag (pure make vars to avoid shell leakage)
XRAY_ARCH_MAP_amd64   = 64
XRAY_ARCH_MAP_386     = 32
XRAY_ARCH_MAP_arm64   = arm64-v8a
XRAY_ARCH_MAP_armv7   = arm32-v7a
XRAY_ARCH_MAP_arm     = arm32-v7a
XRAY_ARCH_MAP_armv6   = arm32-v6
XRAY_ARCH_MAP_armv5   = arm32-v5
XRAY_ARCH_MAP_mips    = mips32
XRAY_ARCH_MAP_mipsle  = mips32le
XRAY_ARCH_MAP_mips64  = mips64
XRAY_ARCH_MAP_mips64le= mips64le
XRAY_ARCH_MAP_ppc64   = ppc64
XRAY_ARCH_MAP_ppc64le = ppc64le
XRAY_ARCH_MAP_riscv64 = riscv64
XRAY_ARCH_MAP_s390x   = s390x

XRAY_VERSION ?= $(shell sed -n 's/^ARG XRAY_VERSION=//p' Dockerfile)
INSTALL_CORE_SHA ?= 1fce1b19aaa54449ec0abdc4098b49027a170705
INSTALL_CORE_URL = https://github.com/PasarGuard/scripts/raw/$(INSTALL_CORE_SHA)/install_core.sh

XRAY_OS_EFFECTIVE   := $(if $(XRAY_OS),$(XRAY_OS),$(GOOS))
XRAY_ARCH_EFFECTIVE := $(if $(XRAY_ARCH),$(XRAY_ARCH),$(XRAY_ARCH_MAP_$(GOARCH)))
XRAY_INSTALL_ARGS   := $(strip $(if $(XRAY_VERSION),--tag $(XRAY_VERSION)) $(if $(XRAY_OS_EFFECTIVE),--os $(XRAY_OS_EFFECTIVE)) $(if $(XRAY_ARCH_EFFECTIVE),--arch $(XRAY_ARCH_EFFECTIVE)))

ifeq ($(GOOS),windows)
OUTPUT = $(NAME).exe
ADDITION = go build -o w$(NAME).exe -trimpath -ldflags "-H windowsgui $(LDFLAGS)" -v $(MAIN)
else
OUTPUT = $(NAME)
endif

ifeq ($(shell echo "$(GOARCH)" | grep -Eq "(mips|mipsle)" && echo true),true)
ADDITION = GOMIPS=softfloat go build -o $(NAME)_softfloat -trimpath -ldflags "$(LDFLAGS)" -v $(MAIN)
endif

.PHONY: clean build build_xray test_xray_patches test test-race-wireguard test-integration test-integration-full test-integration-wireguard

build:
	CGO_ENABLED=0 go build -o $(OUTPUT) $(PARAMS) $(MAIN)
	$(ADDITION)

clean:
	go clean -v -i $(PWD)
	rm -f $(NAME)-* w$(NAME)-*.exe

deps:
	go mod download
	go mod tidy

generate_grpc_code:
	protoc \
	--go_out=. \
	--go_opt=paths=source_relative \
	--go-grpc_out=. \
	--go-grpc_opt=paths=source_relative \
	common/service.proto

CN ?= localhost
SAN ?= DNS:localhost,IP:127.0.0.1

generate_server_cert:
	mkdir -p ./certs
	openssl req -x509 -newkey ec \
		-pkeyopt ec_paramgen_curve:P-256 \
		-keyout ./certs/ssl_key.pem \
		-out ./certs/ssl_cert.pem \
		-days 3650 -nodes \
		-subj "/CN=$(CN)" \
		-addext "subjectAltName = $(SAN)"

generate_client_cert:
	mkdir -p ./certs
	openssl req -x509 -newkey ec \
		-pkeyopt ec_paramgen_curve:P-256 \
		-keyout ./certs/ssl_client_key.pem \
		-out ./certs/ssl_client_cert.pem \
		-days 3650 -nodes \
		-subj "/CN=$(CN)" \
		-addext "subjectAltName = $(SAN)"

UNAME_S := $(shell uname -s)
UNAME_M := $(shell uname -m)
DISTRO := $(shell . /etc/os-release 2>/dev/null && echo $$ID || echo "unknown")

update_os:
ifeq ($(UNAME_S),Linux)
	@echo "Detected OS: Linux"
	@echo "Distribution: $(DISTRO)"

	# Debian/Ubuntu
	if [ "$(DISTRO)" = "debian" ] || [ "$(DISTRO)" = "ubuntu" ]; then \
		sudo apt-get update && \
		sudo apt-get install -y curl bash; \
	fi

	# Alpine Linux
	if [ "$(DISTRO)" = "alpine" ]; then \
		apk update && \
		apk add --no-cache curl bash; \
	fi

	# CentOS/RHEL/Fedora
	if [ "$(DISTRO)" = "centos" ] || [ "$(DISTRO)" = "rhel" ] || [ "$(DISTRO)" = "fedora" ]; then \
		sudo yum update -y && \
		sudo yum install -y curl bash; \
	fi

	# Arch Linux
	if [ "$(DISTRO)" = "arch" ]; then \
		sudo pacman -Sy --noconfirm curl bash; \
	fi
else
	@echo "Unsupported operating system: $(UNAME_S)"
	@exit 1
endif

install_xray: update_os
ifeq ($(UNAME_S),Linux)
	# Debian/Ubuntu, CentOS, Fedora, Arch → Use sudo
	if [ "$(DISTRO)" = "debian" ] || [ "$(DISTRO)" = "ubuntu" ] || \
	   [ "$(DISTRO)" = "centos" ] || [ "$(DISTRO)" = "rhel" ] || [ "$(DISTRO)" = "fedora" ] || \
	   [ "$(DISTRO)" = "arch" ]; then \
		curl -fL $(INSTALL_CORE_URL) | sudo bash -s -- $(XRAY_INSTALL_ARGS); \
	else \
		curl -fL $(INSTALL_CORE_URL) | bash -s -- $(XRAY_INSTALL_ARGS); \
	fi

else
	@echo "Unsupported operating system: $(UNAME_S)"
	@exit 1
endif

XRAY_REPO ?= https://github.com/XTLS/Xray-core.git
XRAY_COMMIT ?= $(shell sed -n 's/^ARG XRAY_COMMIT=//p' Dockerfile)
XRAY_SRC ?= $(CURDIR)/xray-core
XRAY_OUTPUT ?= $(XRAY_SRC)/xray

build_xray:
	test -n "$(XRAY_COMMIT)" && test -n "$(XRAY_SRC)"
	rm -rf "$(XRAY_SRC)"
	git init -q "$(XRAY_SRC)"
	git -C "$(XRAY_SRC)" remote add origin $(XRAY_REPO)
	git -C "$(XRAY_SRC)" fetch -q --depth 1 origin $(XRAY_COMMIT)
	git -C "$(XRAY_SRC)" checkout -q FETCH_HEAD
	test "$$(git -C "$(XRAY_SRC)" rev-parse HEAD)" = "$(XRAY_COMMIT)"
	for patch in $(CURDIR)/patches/xray-*.patch; do git -C "$(XRAY_SRC)" apply --verbose "$$patch" || exit 1; done
	cd "$(XRAY_SRC)" && echo "xray $(XRAY_COMMIT) needs go $$(sed -n 's/^go //p' go.mod); building with $$(GOTOOLCHAIN=local go version)"
	cd "$(XRAY_SRC)" && GOTOOLCHAIN=local CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -buildid=" -o "$(XRAY_OUTPUT)" ./main

test_xray_patches:
	cd "$(XRAY_SRC)" && GOTOOLCHAIN=local go test -count=1 ./app/proxyman/sessionrevoke/...
	cd "$(XRAY_SRC)" && GOTOOLCHAIN=local go test -count=1 -run 'TestSniffQUICShortInitialNoPanic' ./common/protocol/quic/
	cd "$(XRAY_SRC)" && GOTOOLCHAIN=local go test -count=1 -run 'TestForgedXForwardedForIgnoredWithoutTrust|TestDialWithRemoteAddr' ./transport/internet/websocket/ ./transport/internet/httpupgrade/ ./transport/internet/splithttp/
	cd "$(XRAY_SRC)" && GOTOOLCHAIN=local go test -count=1 -run 'TestApplyTrustedXForwardedFor|TestRemoteAddrFromContext' ./common/protocol/http/ ./transport/internet/grpc/encoding/
	cd "$(XRAY_SRC)" && GOTOOLCHAIN=local go test -count=1 -run 'TestTrustedHeaderScenarios|TestInvalidCIDRListLogsOnce' ./common/protocol/http/ ./transport/internet/grpc/encoding/ ./transport/internet/websocket/ ./transport/internet/httpupgrade/ ./transport/internet/splithttp/
	cd "$(XRAY_SRC)" && GOTOOLCHAIN=local go test -count=1 -run 'TestBuildReqMsgsOverlongNameDoesNotPanic|Test_buildReqMsgs' ./app/dns/
	cd "$(XRAY_SRC)" && GOTOOLCHAIN=local go test -count=1 -run 'TestReadResponseAndHandle100Continue' ./proxy/http/
	cd "$(XRAY_SRC)" && GOTOOLCHAIN=local go test -count=1 -run 'TestInvalidDestinationIsRejectedWithoutPanic' ./proxy/shadowsocks_2022/
	cd "$(XRAY_SRC)" && CGO_ENABLED=1 GOTOOLCHAIN=local go test -race -count=3 -run 'TestPerUserStatsCountEveryByteOnceUnderConcurrentFirstTrafficAndReset|TestRuleAndBalancerUpdatesRaceWithRoutePicking' ./app/dispatcher/ ./app/router/
	cd "$(XRAY_SRC)" && GOTOOLCHAIN=local go test -count=1 -run 'TestTunnelOnUnixSocketWithoutPortDoesNotPanic' ./proxy/dokodemo/
	cd "$(XRAY_SRC)" && CGO_ENABLED=1 GOTOOLCHAIN=local go test -race -count=3 -timeout 300s -run 'TestCloseWithPacketInFlightDoesNotPanic|TestProcessAfterCloseDoesNotPanic|TestProcessWhileClosingDoesNotRaceOrPanic' ./proxy/wireguard/ ./proxy/wireguard/gvisortun/

install_wg: update_os
ifeq ($(UNAME_S),Linux)
	@echo "Installing WireGuard for $(DISTRO)..."
	if [ "$(DISTRO)" = "debian" ] || [ "$(DISTRO)" = "ubuntu" ]; then \
		sudo apt-get update && sudo apt-get install -y wireguard wireguard-tools; \
	elif [ "$(DISTRO)" = "centos" ] || [ "$(DISTRO)" = "rhel" ]; then \
		sudo yum install -y epel-release && sudo yum install -y wireguard-tools; \
	elif [ "$(DISTRO)" = "fedora" ]; then \
		sudo dnf install -y wireguard-tools; \
	elif [ "$(DISTRO)" = "arch" ] || [ "$(DISTRO)" = "manjaro" ]; then \
		sudo pacman -Sy --noconfirm wireguard-tools; \
	elif [ "$(DISTRO)" = "alpine" ]; then \
		apk add --no-cache wireguard-tools; \
	elif [ "$(DISTRO)" = "opensuse" ] || [ "$(DISTRO)" = "opensuse-leap" ] || [ "$(DISTRO)" = "opensuse-tumbleweed" ]; then \
		sudo zypper install -y wireguard-tools; \
	else \
		echo "Unsupported distribution: $(DISTRO)"; \
		exit 1; \
	fi
	@echo "WireGuard installed successfully"
	@wg --version
else
	@echo "Unsupported operating system: $(UNAME_S)"
	@exit 1
endif

test-integration:
	$(MAKE) test-integration-full

test-integration-full:
	GOTOOLCHAIN=auto TEST_INTEGRATION=true go test ./... -v -p 1
	GOTOOLCHAIN=auto go test -tags=integration -v -p 1 ./backend/wireguard
	$(MAKE) test-integration-mtproto

test-integration-mtproto:
	MTPROTO_SMOKE_FRONTING=1 GOTOOLCHAIN=auto TEST_INTEGRATION=false go test -count=1 -v -p 1 -run TestLiveSmoke_MTProto ./backend/mtproto

test-integration-wireguard:
	GOTOOLCHAIN=auto go test -tags=integration -v -p 1 ./backend/wireguard

test:
	GOTOOLCHAIN=auto TEST_INTEGRATION=false go test ./... -v -p 1

test-race-wireguard:
	GOTOOLCHAIN=auto CGO_ENABLED=1 go test -race -v -p 1 ./backend/wireguard

serve:
	go run ./cmd/node
