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

GEOFILES_TAG ?= $(shell sed -n 's/^ARG GEOFILES_TAG=//p' Dockerfile)
GEOIP_SHA256 ?= $(shell sed -n 's/^ARG GEOIP_SHA256=//p' Dockerfile)
GEOSITE_SHA256 ?= $(shell sed -n 's/^ARG GEOSITE_SHA256=//p' Dockerfile)
GEOFILES_REPO ?= Loyalsoldier/v2ray-rules-dat
GEOFILES_DIR ?= /usr/local/share/xray
GEOFILES_XRAY ?= /usr/local/bin/xray
GEOFILES_CHECK_CONFIG ?= $(CURDIR)/testdata/geofiles-check.json

ifeq ($(GOOS),windows)
OUTPUT = $(NAME).exe
ADDITION = go build -o w$(NAME).exe -trimpath -ldflags "-H windowsgui $(LDFLAGS)" -v $(MAIN)
else
OUTPUT = $(NAME)
endif

ifeq ($(shell echo "$(GOARCH)" | grep -Eq "(mips|mipsle)" && echo true),true)
ADDITION = GOMIPS=softfloat go build -o $(NAME)_softfloat -trimpath -ldflags "$(LDFLAGS)" -v $(MAIN)
endif

.PHONY: clean build build_xray build_singbox test_singbox_http2_guard test_xray_guards test_xray_patches test test-race-wireguard test-integration test-integration-full test-integration-wireguard test-drift \
	install_geofiles check_geofiles update_geofiles_pin

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
	@$(MAKE) --no-print-directory install_geofiles

else
	@echo "Unsupported operating system: $(UNAME_S)"
	@exit 1
endif

GEOFILES_SUDO ?= $(if $(filter debian ubuntu centos rhel fedora arch,$(DISTRO)),sudo)

install_geofiles:
	@set -e; \
	case "$(GEOFILES_TAG)" in ""|*[!0-9]*) echo "error: GEOFILES_TAG '$(GEOFILES_TAG)' is not a release tag" >&2; exit 1 ;; esac; \
	for want in "$(GEOIP_SHA256)" "$(GEOSITE_SHA256)"; do \
		if ! printf '%s\n' "$$want" | grep -Eqx '[0-9a-f]{64}'; then echo "error: '$$want' is not a sha256" >&2; exit 1; fi; \
	done; \
	tmp=$$(mktemp -d); \
	trap 'rm -rf "$$tmp"' EXIT; \
	url="https://github.com/$(GEOFILES_REPO)/releases/download/$(GEOFILES_TAG)"; \
	echo "Installing geo files from $(GEOFILES_REPO) $(GEOFILES_TAG) into $(GEOFILES_DIR)"; \
	fetch() { \
		ok=0; \
		for try in 1 2 3; do \
			if curl -fsSL "$$url/$$1" -o "$$tmp/$$1"; then ok=1; break; fi; \
			sleep 2; \
		done; \
		if [ "$$ok" -ne 1 ]; then \
			echo "error: cannot download $$1 from $(GEOFILES_REPO) $(GEOFILES_TAG)" >&2; \
			exit 1; \
		fi; \
		got=$$(sha256sum "$$tmp/$$1" | cut -d ' ' -f 1); \
		if [ "$$got" != "$$2" ]; then \
			echo "error: $$1 from $(GEOFILES_REPO) $(GEOFILES_TAG) has sha256 $$got, expected $$2" >&2; \
			exit 1; \
		fi; \
		echo "$$1: sha256 OK ($$got)"; \
	}; \
	fetch geoip.dat "$(GEOIP_SHA256)"; \
	fetch geosite.dat "$(GEOSITE_SHA256)"; \
	$(GEOFILES_SUDO) install -d "$(GEOFILES_DIR)"; \
	$(GEOFILES_SUDO) install -m 644 "$$tmp/geoip.dat" "$(GEOFILES_DIR)/geoip.dat"; \
	$(GEOFILES_SUDO) install -m 644 "$$tmp/geosite.dat" "$(GEOFILES_DIR)/geosite.dat"
	@$(MAKE) --no-print-directory check_geofiles

check_geofiles:
	@set -e; \
	host_os=$$(go env GOHOSTOS 2>/dev/null || uname -s | tr '[:upper:]' '[:lower:]'); \
	host_arch=$$(go env GOHOSTARCH 2>/dev/null || case "$$(uname -m)" in (x86_64) echo amd64 ;; (aarch64|arm64) echo arm64 ;; (*) uname -m ;; esac); \
	if { [ -n "$(GOOS)" ] && [ "$(GOOS)" != "$$host_os" ]; } || \
	   { [ -n "$(GOARCH)" ] && [ "$(GOARCH)" != "$$host_arch" ]; }; then \
		echo "Skipping the geo files check: cross build for $(GOOS)/$(GOARCH), $(GEOFILES_XRAY) cannot run on this host"; \
		exit 0; \
	fi; \
	status=0; \
	out=$$(XRAY_LOCATION_ASSET="$(GEOFILES_DIR)" "$(GEOFILES_XRAY)" run -test -c "$(GEOFILES_CHECK_CONFIG)" 2>&1) || status=$$?; \
	printf '%s\n' "$$out"; \
	case "$$out" in *"Configuration OK"*) ;; *) status=1 ;; esac; \
	if [ "$$status" -ne 0 ]; then \
		echo "error: $(GEOFILES_XRAY) cannot load $(GEOFILES_CHECK_CONFIG) with the geo files in $(GEOFILES_DIR)" >&2; \
		exit 1; \
	fi; \
	echo "Geo files check passed: $(GEOFILES_DIR)"

update_geofiles_pin:
	@set -e; \
	tmp=$$(mktemp -d); \
	trap 'rm -rf "$$tmp"' EXIT; \
	latest=$$(curl -fsSIL -o /dev/null -w '%{url_effective}' "https://github.com/$(GEOFILES_REPO)/releases/latest"); \
	tag=$${latest##*/releases/tag/}; \
	case "$$tag" in ""|*/*) echo "error: cannot resolve the latest $(GEOFILES_REPO) release from $$latest" >&2; exit 1 ;; esac; \
	url="https://github.com/$(GEOFILES_REPO)/releases/download/$$tag"; \
	for name in geoip.dat geosite.dat; do \
		curl -fsSL --retry 3 --retry-connrefused "$$url/$$name" -o "$$tmp/$$name"; \
		curl -fsSL --retry 3 --retry-connrefused "$$url/$$name.sha256sum" -o "$$tmp/$$name.sha256sum"; \
		(cd "$$tmp" && sha256sum -c "$$name.sha256sum") >&2; \
	done; \
	echo "ARG GEOFILES_TAG=$$tag"; \
	echo "ARG GEOIP_SHA256=$$(sha256sum "$$tmp/geoip.dat" | cut -d ' ' -f 1)"; \
	echo "ARG GEOSITE_SHA256=$$(sha256sum "$$tmp/geosite.dat" | cut -d ' ' -f 1)"

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

SINGBOX_REPO ?= $(shell sed -n 's/^ARG SINGBOX_REPO=//p' Dockerfile)
SINGBOX_COMMIT ?= $(shell sed -n 's/^ARG SINGBOX_COMMIT=//p' Dockerfile)
SINGBOX_VERSION ?= $(shell sed -n 's/^ARG SINGBOX_VERSION=//p' Dockerfile)
SINGBOX_TAGS ?= $(shell sed -n 's/^ARG SINGBOX_TAGS=//p' Dockerfile)
SINGBOX_SRC ?= $(CURDIR)/sing-box-src
SINGBOX_OUTPUT ?= $(SINGBOX_SRC)/sing-box

build_singbox: test_singbox_http2_guard
	test -n "$(SINGBOX_REPO)" && test -n "$(SINGBOX_COMMIT)" && test -n "$(SINGBOX_VERSION)" && test -n "$(SINGBOX_TAGS)" && test -n "$(SINGBOX_SRC)"
	rm -rf "$(SINGBOX_SRC)"
	git init -q "$(SINGBOX_SRC)"
	git -C "$(SINGBOX_SRC)" remote add origin $(SINGBOX_REPO)
	git -C "$(SINGBOX_SRC)" fetch -q --depth 1 origin $(SINGBOX_COMMIT)
	git -C "$(SINGBOX_SRC)" checkout -q FETCH_HEAD
	test "$$(git -C "$(SINGBOX_SRC)" rev-parse HEAD)" = "$(SINGBOX_COMMIT)"
	cd "$(SINGBOX_SRC)" && GOTOOLCHAIN=local CGO_ENABLED=0 sh $(CURDIR)/scripts/singbox_http2_guard.sh "$(SINGBOX_TAGS)"
	cd "$(SINGBOX_SRC)" && GOTOOLCHAIN=local CGO_ENABLED=0 go build -trimpath -tags "$(SINGBOX_TAGS)" -ldflags "-X 'github.com/sagernet/sing-box/constant.Version=$(SINGBOX_VERSION)' -s -w -buildid=" -o "$(SINGBOX_OUTPUT)" ./cmd/sing-box
	"$(SINGBOX_OUTPUT)" version

test_singbox_http2_guard:
	bash scripts/test_singbox_http2_guard.sh

XRAY_GUARD_SRC ?= $(CURDIR)/xray-guard-src

test_xray_guards:
	python3 -B scripts/test_xray_guard_replay.py
	test -n "$(XRAY_COMMIT)" && test -n "$(XRAY_GUARD_SRC)"
	rm -rf "$(XRAY_GUARD_SRC)"
	git init -q "$(XRAY_GUARD_SRC)"
	git -C "$(XRAY_GUARD_SRC)" remote add origin $(XRAY_REPO)
	git -C "$(XRAY_GUARD_SRC)" fetch -q --depth 1 origin $(XRAY_COMMIT)
	git -C "$(XRAY_GUARD_SRC)" checkout -q FETCH_HEAD
	python3 scripts/xray_guard_replay.py "$(CURDIR)/Dockerfile" "$(CURDIR)/patches" "$(XRAY_GUARD_SRC)" "$(XRAY_COMMIT)" "$(XRAY_GUARD_ONLY)"

test_xray_patches:
	cd "$(XRAY_SRC)" && CGO_ENABLED=1 GOTOOLCHAIN=local go test -race -count=1 ./app/proxyman/sessionrevoke/...
	cd "$(XRAY_SRC)" && CGO_ENABLED=1 GOTOOLCHAIN=local go test -race -count=3 -run 'TestConcurrentBindReleaseAndRevoke|TestALateDispatchOfARevokedUserIsRefusedAfterItsFlowWasReleased|TestLateDispatchesRacingRevokeLeaveNoReleasedSessionRegistered' ./app/proxyman/sessionrevoke/
	cd "$(XRAY_SRC)" && CGO_ENABLED=1 GOTOOLCHAIN=local go test -race -count=3 -run 'TestRevokeCutsAUDPFlowAndKeepsTheSourcesNextFlow|TestRevokeRacingUDPPacketsNewFlowsAndReAddsCutsOnlyRevokedGenerations|TestAUDPFlowStopsDispatchingOnceItsContextIsCancelled|TestADispatchThatOutlivesItsUDPFlowLeavesNoSessionRegistered' ./app/proxyman/inbound/ ./proxy/shadowsocks/
	cd "$(XRAY_SRC)" && CGO_ENABLED=1 GOTOOLCHAIN=local go test -race -count=3 -run 'TestAShadowsocks2022UDPFlowEndedByAnInvalidPacketLeavesNoSessionRegistered|TestAShadowsocks2022UDPFlowEndedByADuplicatedDatagramLeavesNoSessionRegistered' ./app/proxyman/inbound/
	cd "$(XRAY_SRC)" && CGO_ENABLED=1 GOTOOLCHAIN=local go test -race -count=3 -run 'TestUDPConnCancelIsPublishedUnderTheWorkerLockBeforeCleanClosesIt' ./app/proxyman/inbound/
	cd "$(XRAY_SRC)" && CGO_ENABLED=1 GOTOOLCHAIN=local go test -race -count=1 -run 'TestSniffQUICShortInitialNoPanic|TestSniffQUICHugeTokenLengthNoPanic' ./common/protocol/quic/
	cd "$(XRAY_SRC)" && CGO_ENABLED=1 GOTOOLCHAIN=local go test -race -count=1 -run 'TestForgedXForwardedForIgnoredWithoutTrust|TestDialWithRemoteAddr' ./transport/internet/websocket/ ./transport/internet/httpupgrade/ ./transport/internet/splithttp/
	cd "$(XRAY_SRC)" && CGO_ENABLED=1 GOTOOLCHAIN=local go test -race -count=10 -run 'TestDialWithRemoteAddr|TestAStreamDownBodySetWhileAReadWaitsIsDeliveredWhole|TestAStreamClosedBeforeItsResponseClosesTheLateBodyOnce|TestConcurrentClosesRacingTheResponseCloseItsBodyOnceAndWakeTheWaitingRead|TestAFailedStreamDownMarksTheClientClosedWhileOthersPollIt|TestUploadWriterCountsEachWriteWhileTheUploaderReleasesItsBuffers|TestUploadWriterDeliversLargeWritesExactlyOnceToAConcurrentUploader|TestConcurrentStreamsOpenReadAndCloseWithoutLosingOrDuplicatingData' ./transport/internet/splithttp/
	cd "$(XRAY_SRC)" && CGO_ENABLED=1 GOTOOLCHAIN=local go test -race -count=1 -run 'TestApplyTrustedXForwardedFor|TestRemoteAddrFromContext' ./common/protocol/http/ ./transport/internet/grpc/encoding/
	cd "$(XRAY_SRC)" && CGO_ENABLED=1 GOTOOLCHAIN=local go test -race -count=1 -run 'TestTrustedHeaderScenarios|TestInvalidCIDRListLogsOnce|TestRepeatedOrListTrustedHeaderFallsBackToThePeer' ./common/protocol/http/ ./transport/internet/grpc/encoding/ ./transport/internet/websocket/ ./transport/internet/httpupgrade/ ./transport/internet/splithttp/
	cd "$(XRAY_SRC)" && CGO_ENABLED=1 GOTOOLCHAIN=local go test -race -count=1 -run 'TestBuildReqMsgsOverlongNameDoesNotPanic|Test_buildReqMsgs' ./app/dns/
	cd "$(XRAY_SRC)" && CGO_ENABLED=1 GOTOOLCHAIN=local go test -race -count=1 -run 'TestReadResponseAndHandle100Continue' ./proxy/http/
	cd "$(XRAY_SRC)" && CGO_ENABLED=1 GOTOOLCHAIN=local go test -race -count=3 -run 'TestPinnedCAWithoutServerNameRefusesALeafIssuedForAnyName|TestPinnedCARefusesALeafIssuedForAnotherServerName|TestPinnedCAAcceptsALeafIssuedForTheServerName|TestPinnedLeafStillNeedsNoServerName|TestDestinationGivesThePinnedCAAServerName' ./transport/internet/tls/
	cd "$(XRAY_SRC)" && CGO_ENABLED=1 GOTOOLCHAIN=local go test -race -count=1 -run 'TestParsedDestinationIsNeverAnEmptyAddress' ./proxy/shadowsocks_2022/
	cd "$(XRAY_SRC)" && CGO_ENABLED=1 GOTOOLCHAIN=local go test -race -count=3 -run 'TestAUserWhoseCredentialAnotherUserHoldsIsRefused|TestRemovingAUserLeavesACredentialAnotherUserHolds|TestAUserReaddedWithItsOwnCredentialKeepsIt|TestUsersWithoutAnEmailMayShareACredential|TestEveryHeldUserAuthenticatesAsItselfUnderChurn|TestAUserTheValidatorRefusesLeavesNoEmailEntry|TestRefusedAddsRacingARemoveAndReaddKeepEveryEmailEntryTrue' ./proxy/vless/ ./proxy/trojan/ ./proxy/vmess/ ./proxy/vmess/inbound/
	cd "$(XRAY_SRC)" && CGO_ENABLED=1 GOTOOLCHAIN=local go test -race -count=3 -run 'TestPerUserStatsCountEveryByteOnceUnderConcurrentFirstTrafficAndReset|TestRuleAndBalancerUpdatesRaceWithRoutePicking|TestConcurrentRuleWritersLoseNoUpdateWhileRoutesArePicked' ./app/dispatcher/ ./app/router/
	cd "$(XRAY_SRC)" && GOTOOLCHAIN=local go test -race=false -count=1 -run 'TestAddingALargeDomainRuleDoesNotStallRoutePicking|TestRemovingAWebhookRuleDoesNotStallRoutePicking' ./app/router/
	cd "$(XRAY_SRC)" && CGO_ENABLED=1 GOTOOLCHAIN=local go test -race -count=1 -run 'TestTunnelOnUnixSocketWithoutPortDoesNotPanic' ./proxy/dokodemo/
	cd "$(XRAY_SRC)" && CGO_ENABLED=1 GOTOOLCHAIN=local go test -race -count=1 -run 'TestEarlyDataConnAddressesBeforeFirstWrite' ./transport/internet/websocket/
	cd "$(XRAY_SRC)" && CGO_ENABLED=1 GOTOOLCHAIN=local go test -race -count=3 -timeout 300s -run 'TestCloseWithPacketInFlightDoesNotPanic|TestClosingABindTwiceDoesNotPanic|TestADialThroughAClosedTunDoesNotPanic|TestAClosedHandlerRefusesToComeUp' ./proxy/wireguard/

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

test-drift:
	bash scripts/upstream_drift_test.sh
	bash scripts/upstream_drift_bypass_test.sh
