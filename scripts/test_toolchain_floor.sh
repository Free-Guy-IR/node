#!/usr/bin/env bash
set -eu
set -o pipefail

repo_root=$(git rev-parse --show-toplevel)
guard=${TOOLCHAIN_FLOOR_GUARD:-$repo_root/scripts/toolchain_floor.sh}
[ -f "$guard" ] || { printf 'test-toolchain-floor: %s is missing\n' "$guard" >&2; exit 2; }
guard=$(cd "$(dirname "$guard")" && pwd)/$(basename "$guard")

tmp=$(mktemp -d "${TMPDIR:-/tmp}/toolchain-floor-test.XXXXXX")
trap 'rm -rf "$tmp"' EXIT

pass=0
failed=0
digest=sha256:27f2d3b83cfc5803c8078f8e730af0f7c431bf067afaec8f60a36fb763187cba
alpine=sha256:294b683cb724975bec92580e1e685676bd4b50bda910ddb8c51d4cabeaec77e6

gomod_patch() {
  printf 'Move x/net\n\ndiff --git a/go.mod b/go.mod\n--- a/go.mod\n+++ b/go.mod\n@@ -1,3 +1,3 @@\n-\tgolang.org/x/net %s\n+\tgolang.org/x/net %s\n diff --git a/go.sum b/go.sum\n--- a/go.sum\n+++ b/go.sum\n@@ -1,2 +1,2 @@\n-golang.org/x/net %s h1:old=\n+golang.org/x/net %s h1:new=\n' "$1" "$2" "$1" "$2"
}

fixture() {
  r=$tmp/$1
  mkdir -p "$r/.github/workflows" "$r/patches"
  printf 'module example.com/node\n\ngo 1.26.3\n\ntoolchain go1.26.10\n\nrequire (\n\tgolang.org/x/sys v0.48.0\n)\n\nrequire (\n\tgolang.org/x/net v0.60.0 // indirect\n\tgolang.org/x/text v0.42.0 // indirect\n)\n' >"$r/go.mod"
  cat >"$r/.github/workflows/run-tests.yml" <<'EOF'
name: Test
on: push
jobs:
  test:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v7
      - name: Set up Go
        uses: actions/setup-go@v7
        with:
          go-version: "1.26.10"
          check-latest: true
  govulncheck:
    runs-on: ubuntu-latest
    steps:
      - name: Set up Go
        uses: actions/setup-go@v7
        with:
          go-version: "1.26.10"
EOF
  cat >"$r/.github/workflows/publish-release.yml" <<'EOF'
name: Release
on: push
jobs:
  build:
    runs-on: ubuntu-latest
    steps:
      - name: Set up Go
        uses: actions/setup-go@v7
        with:
          go-version: "1.26.10"
EOF
  printf 'name: Image\non: push\njobs:\n  image:\n    runs-on: ubuntu-latest\n    steps:\n      - run: docker build .\n' >"$r/.github/workflows/image.yml"
  cat >"$r/Dockerfile" <<EOF
ARG XRAY_VERSION=v26.3.27

FROM --platform=\$BUILDPLATFORM golang:1.26.10-alpine@$digest AS builder
RUN make build

FROM --platform=\$BUILDPLATFORM golang:1.26.10-alpine@$digest AS singbox-builder
RUN go build ./cmd/sing-box

FROM --platform=\$BUILDPLATFORM golang:1.26.10-alpine@$digest AS xray-builder
COPY patches/xray-grpc-1.83.2.patch /patches/xray-grpc-1.83.2.patch
COPY patches/xray-session-revoke.patch /patches/xray-session-revoke.patch
COPY patches/xray-x-net-0.60.0.patch /patches/xray-x-net-0.60.0.patch
RUN git init -q . \\
 && git apply --verbose /patches/xray-grpc-1.83.2.patch \\
 && grep -q "golang.org/x/net v0.58.0$" go.mod \\
 && git apply --verbose /patches/xray-session-revoke.patch \\
 && git apply --verbose /patches/xray-x-net-0.60.0.patch \\
 && grep -q "golang.org/x/net v0.60.0$" go.mod
RUN go build -o /out/xray ./main

FROM alpine:3.24.2@$alpine
COPY --from=builder /src/main /usr/local/bin/main
EOF
  printf "FROM --platform=\$BUILDPLATFORM golang:1.26.10-alpine@%s AS builder\nRUN go build ./...\n\nFROM alpine:3.24.2@%s\n" "$digest" "$alpine" >"$r/Dockerfile.wireguard"
  gomod_patch v0.52.0 v0.58.0 >"$r/patches/xray-grpc-1.83.2.patch"
  printf 'Revoke sessions\n\ndiff --git a/app/x.go b/app/x.go\n--- a/app/x.go\n+++ b/app/x.go\n@@ -1 +1 @@\n-a\n+b\n' >"$r/patches/xray-session-revoke.patch"
  gomod_patch v0.58.0 v0.60.0 >"$r/patches/xray-x-net-0.60.0.patch"
}

edit() {
  local file=$1 expr=$2
  sed -E "$expr" "$r/$file" >"$r/$file.new"
  if cmp -s "$r/$file" "$r/$file.new"; then
    printf 'FAIL     %-40s the fixture edit %s on %s changed nothing\n' "$(basename "$r")" "$expr" "$file"
    failed=$((failed + 1))
  fi
  mv "$r/$file.new" "$r/$file"
}

check() {
  name=$1 want=$2 want_text=${3:-}
  r=$tmp/$name
  set +e
  bash "$guard" "$r" >"$tmp/$name.out" 2>&1
  got=$?
  set -e
  if [ "$got" = "$want" ] && { [ -z "$want_text" ] || grep -qF -- "$want_text" "$tmp/$name.out"; }; then
    printf 'ok       %-40s exit %s\n' "$name" "$got"
    pass=$((pass + 1))
  else
    printf 'FAIL     %-40s exit %s, want %s%s\n' "$name" "$got" "$want" "${want_text:+ and output \"$want_text\"}"
    sed 's/^/         | /' "$tmp/$name.out" | tail -8
    failed=$((failed + 1))
  fi
}

fixture clean
check clean 0 "11 pins checked, all at or above Go 1.26.10 and golang.org/x/net v0.60.0"

fixture toolchain-below
edit go.mod 's/^toolchain go1.26.10$/toolchain go1.26.9/'
check toolchain-below 1 "go.mod toolchain names Go 1.26.9, below the security floor 1.26.10"

fixture toolchain-old-release
edit go.mod 's/^toolchain go1.26.10$/toolchain go1.26.6/'
check toolchain-old-release 1 "go.mod toolchain names Go 1.26.6"

fixture toolchain-two-digit-patch
edit go.mod 's/^toolchain go1.26.10$/toolchain go1.26.11/'
check toolchain-two-digit-patch 0

fixture toolchain-three-digit-patch
edit go.mod 's/^toolchain go1.26.10$/toolchain go1.26.100/'
check toolchain-three-digit-patch 0

fixture toolchain-next-minor
edit go.mod 's/^toolchain go1.26.10$/toolchain go1.27.0/'
check toolchain-next-minor 0

fixture toolchain-release-candidate
edit go.mod 's/^toolchain go1.26.10$/toolchain go1.27rc1/'
check toolchain-release-candidate 1 "which is not an exact release"

fixture toolchain-dropped
edit go.mod '/^toolchain /d'
check toolchain-dropped 1 "go.mod go line (no toolchain line) names Go 1.26.3, below the security floor 1.26.10"

fixture toolchain-dropped-go-line-raised
edit go.mod '/^toolchain /d; s/^go 1.26.3$/go 1.26.10/'
check toolchain-dropped-go-line-raised 0

fixture toolchain-twice
printf 'toolchain go1.26.6\n' >>"$tmp/toolchain-twice/go.mod"
check toolchain-twice 1 "more than one toolchain line"

fixture xnet-below
edit go.mod 's/golang.org\/x\/net v0.60.0/golang.org\/x\/net v0.59.0/'
check xnet-below 1 "go.mod uses golang.org/x/net v0.59.0, below the security floor v0.60.0"

fixture xnet-three-digit-minor
edit go.mod 's/golang.org\/x\/net v0.60.0/golang.org\/x\/net v0.100.0/'
check xnet-three-digit-minor 0

fixture xnet-pseudo-version
edit go.mod 's/golang.org\/x\/net v0.60.0/golang.org\/x\/net v0.60.1-0.20261001000000-0123456789ab/'
check xnet-pseudo-version 1 "which is not a tagged release"

fixture xnet-single-line-require
edit go.mod '/golang.org\/x\/net v0.60.0/d'
printf '\nrequire golang.org/x/net v0.58.0\n' >>"$tmp/xnet-single-line-require/go.mod"
check xnet-single-line-require 1 "go.mod uses golang.org/x/net v0.58.0"

fixture xnet-required-twice
printf '\nrequire golang.org/x/net v0.58.0\n' >>"$tmp/xnet-required-twice/go.mod"
check xnet-required-twice 1 "go.mod requires golang.org/x/net more than once"

fixture xnet-dropped
edit go.mod '/golang.org\/x\/net v0.60.0/d'
check xnet-dropped 1 "go.mod does not require golang.org/x/net"

fixture xnet-replaced
printf '\nreplace golang.org/x/net => golang.org/x/net v0.58.0\n' >>"$tmp/xnet-replaced/go.mod"
check xnet-replaced 1 "go.mod replaces golang.org/x/net"

fixture xnet-replaced-in-block
printf '\nreplace (\n\tgolang.org/x/net v0.60.0 => ../net\n)\n' >>"$tmp/xnet-replaced-in-block/go.mod"
check xnet-replaced-in-block 1 "go.mod replaces golang.org/x/net"

fixture ci-one-job-below
edit .github/workflows/run-tests.yml "/^  govulncheck:/,\$s/go-version: \"1.26.10\"/go-version: \"1.26.6\"/"
check ci-one-job-below 1 "run-tests.yml go-version names Go 1.26.6, below the security floor 1.26.10"

fixture ci-unquoted
edit .github/workflows/run-tests.yml 's/go-version: "1.26.10"/go-version: 1.26.10/'
check ci-unquoted 0

fixture ci-single-quoted-below
edit .github/workflows/run-tests.yml "s/go-version: \"1.26.10\"/go-version: '1.26.8'/"
check ci-single-quoted-below 1 "run-tests.yml go-version names Go 1.26.8"

fixture ci-floating-minor
edit .github/workflows/run-tests.yml 's/go-version: "1.26.10"/go-version: "1.26"/'
check ci-floating-minor 1 "run-tests.yml go-version names Go '1.26', which is not an exact release"

fixture ci-floating-alias
edit .github/workflows/run-tests.yml 's/go-version: "1.26.10"/go-version: stable/'
check ci-floating-alias 1 "which is not an exact release"

fixture ci-version-file
edit .github/workflows/run-tests.yml 's/go-version: "1.26.10"/go-version-file: go.mod/'
check ci-version-file 1 "run-tests.yml sets up Go from go-version-file"

fixture ci-setup-without-version
edit .github/workflows/run-tests.yml '/go-version: "1.26.10"/d'
check ci-setup-without-version 1 "run-tests.yml has 2 actions/setup-go steps but 0 go-version pins"

fixture release-below
edit .github/workflows/publish-release.yml 's/go-version: "1.26.10"/go-version: "1.26.9"/'
check release-below 1 "publish-release.yml go-version names Go 1.26.9, below the security floor 1.26.10"

fixture yaml-extension-below
sed 's/1.26.10/1.26.6/' "$tmp/yaml-extension-below/.github/workflows/publish-release.yml" >"$tmp/yaml-extension-below/.github/workflows/extra.yaml"
check yaml-extension-below 1 "extra.yaml go-version names Go 1.26.6"

fixture image-one-stage-below
edit Dockerfile 's/golang:1.26.10-alpine(@[^ ]+) AS xray-builder/golang:1.26.9-alpine\1 AS xray-builder/'
check image-one-stage-below 1 "Dockerfile image golang:1.26.9-alpine names Go 1.26.9, below the security floor 1.26.10"

fixture image-without-digest
edit Dockerfile 's/golang:1.26.10-alpine@[^ ]+ AS singbox-builder/golang:1.26.10-alpine AS singbox-builder/'
check image-without-digest 1 "Dockerfile builds from golang:1.26.10-alpine without an @sha256 digest"

fixture image-floating-tag
edit Dockerfile 's/golang:1.26.10-alpine(@[^ ]+) AS builder/golang:1.26-alpine\1 AS builder/'
check image-floating-tag 1 "names Go '1.26', which is not an exact release"

fixture image-registry-prefix-below
edit Dockerfile 's/golang:1.26.10-alpine(@[^ ]+) AS builder/docker.io\/library\/golang:1.26.6-alpine\1 AS builder/'
check image-registry-prefix-below 1 "Dockerfile image golang:1.26.6-alpine names Go 1.26.6"

fixture image-from-build-arg
edit Dockerfile "s/golang:1.26.10-alpine@[^ ]+ AS builder/\${GO_IMAGE} AS builder/"
check image-from-build-arg 1 "an image chosen by a build argument"

fixture image-lowercase-from
edit Dockerfile "s/^FROM --platform=\\\$BUILDPLATFORM golang:1.26.10-alpine(@[^ ]+) AS builder/from --platform=\$BUILDPLATFORM golang:1.26.6-alpine\\1 AS builder/"
check image-lowercase-from 1 "Dockerfile image golang:1.26.6-alpine names Go 1.26.6"

fixture image-two-digit-patch
edit Dockerfile 's/golang:1.26.10-alpine/golang:1.26.11-alpine/g'
check image-two-digit-patch 0

fixture wireguard-image-below
edit Dockerfile.wireguard 's/golang:1.26.10-alpine/golang:1.26.6-alpine/'
check wireguard-image-below 1 "Dockerfile.wireguard image golang:1.26.6-alpine names Go 1.26.6"

fixture no-golang-stage
edit Dockerfile 's/golang:1.26.10-alpine@[^ ]+/alpine:3.24.2@'"$alpine"'/'
edit Dockerfile.wireguard 's/golang:1.26.10-alpine@[^ ]+/alpine:3.24.2@'"$alpine"'/'
check no-golang-stage 1 "no Dockerfile builds from a golang image"

fixture xray-patch-below
gomod_patch v0.58.0 v0.59.0 >"$tmp/xray-patch-below/patches/xray-x-net-0.60.0.patch"
check xray-patch-below 1 "the Xray binary (Dockerfile patch order) uses golang.org/x/net v0.59.0, below the security floor v0.60.0"

fixture xray-patch-not-applied
edit Dockerfile '/git apply --verbose \/patches\/xray-x-net-0.60.0.patch/d'
check xray-patch-not-applied 1 "the Xray binary (Dockerfile patch order) uses golang.org/x/net v0.58.0"

fixture xray-patch-missing
rm "$tmp/xray-patch-missing/patches/xray-x-net-0.60.0.patch"
check xray-patch-missing 1 "patches/xray-x-net-0.60.0.patch is applied to the Xray build but is missing"

fixture xray-later-patch-lowers
gomod_patch v0.60.0 v0.58.0 >"$tmp/xray-later-patch-lowers/patches/xray-zz-lower.patch"
edit Dockerfile 's@^ && git apply --verbose /patches/xray-x-net-0.60.0.patch \\$@ \&\& git apply --verbose /patches/xray-x-net-0.60.0.patch \&\& git apply --verbose /patches/xray-zz-lower.patch \\@'
check xray-later-patch-lowers 1 "the Xray binary (Dockerfile patch order) uses golang.org/x/net v0.58.0"

fixture make-glob-lowers
gomod_patch v0.60.0 v0.58.0 >"$tmp/make-glob-lowers/patches/xray-zz-lower.patch"
check make-glob-lowers 1 "the Xray binary (make build_xray patch order) uses golang.org/x/net v0.58.0"

fixture make-glob-ignores-other-files
gomod_patch v0.60.0 v0.58.0 >"$tmp/make-glob-ignores-other-files/patches/README.patch"
check make-glob-ignores-other-files 0

fixture xray-no-xnet-patch
printf 'Other\n\ndiff --git a/app/y.go b/app/y.go\n--- a/app/y.go\n+++ b/app/y.go\n@@ -1 +1 @@\n-a\n+b\n' >"$tmp/xray-no-xnet-patch/patches/xray-grpc-1.83.2.patch"
cp "$tmp/xray-no-xnet-patch/patches/xray-grpc-1.83.2.patch" "$tmp/xray-no-xnet-patch/patches/xray-x-net-0.60.0.patch"
check xray-no-xnet-patch 1 "no patch applied by the Dockerfile xray-builder stage sets golang.org/x/net"

fixture xray-gosum-only-is-not-gomod
printf 'Sum only\n\ndiff --git a/go.sum b/go.sum\n--- a/go.sum\n+++ b/go.sum\n@@ -1 +1 @@\n-golang.org/x/net v0.60.0 h1:a=\n+\tgolang.org/x/net v0.58.0 h1:b=\n' >"$tmp/xray-gosum-only-is-not-gomod/patches/xray-zz-sum-only.patch"
check xray-gosum-only-is-not-gomod 0

fixture xray-patch-sets-xnet-twice
{ gomod_patch v0.58.0 v0.60.0; printf 'diff --git a/go.mod b/go.mod\n--- a/go.mod\n+++ b/go.mod\n@@ -9,1 +9,1 @@\n-\tgolang.org/x/net v0.60.0\n+\tgolang.org/x/net v0.58.0\n'; } >"$tmp/xray-patch-sets-xnet-twice/patches/xray-x-net-0.60.0.patch"
check xray-patch-sets-xnet-twice 1 "patches/xray-x-net-0.60.0.patch sets golang.org/x/net in go.mod more than once"

fixture base-tree-shape
edit go.mod 's/^toolchain go1.26.10$/toolchain go1.26.6/; s/golang.org\/x\/net v0.60.0/golang.org\/x\/net v0.58.0/'
edit .github/workflows/run-tests.yml 's/1.26.10/1.26.6/'
edit .github/workflows/publish-release.yml 's/1.26.10/1.26.6/'
edit Dockerfile 's/golang:1.26.10-alpine/golang:1.26.6-alpine/'
edit Dockerfile.wireguard 's/golang:1.26.10-alpine/golang:1.26.6-alpine/'
gomod_patch v0.58.0 v0.59.0 >"$tmp/base-tree-shape/patches/xray-x-net-0.60.0.patch"
check base-tree-shape 1 "11 problems in 11 pins"

printf 'test-toolchain-floor: %d passed, %d failed\n' "$pass" "$failed"
[ "$failed" = 0 ]
