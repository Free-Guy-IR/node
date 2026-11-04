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
edit Dockerfile '/grep -q "golang.org\/x\/net /d'
check xray-no-xnet-patch 1 "neither a patch applied by the Dockerfile xray-builder stage nor a go.mod check in it sets golang.org/x/net"

fixture xray-gosum-only-is-not-gomod
printf 'Sum only\n\ndiff --git a/go.sum b/go.sum\n--- a/go.sum\n+++ b/go.sum\n@@ -1 +1 @@\n-golang.org/x/net v0.60.0 h1:a=\n+\tgolang.org/x/net v0.58.0 h1:b=\n' >"$tmp/xray-gosum-only-is-not-gomod/patches/xray-zz-sum-only.patch"
check xray-gosum-only-is-not-gomod 0

fixture xray-patch-sets-xnet-twice
{ gomod_patch v0.58.0 v0.60.0; printf 'diff --git a/go.mod b/go.mod\n--- a/go.mod\n+++ b/go.mod\n@@ -9,1 +9,1 @@\n-\tgolang.org/x/net v0.60.0\n+\tgolang.org/x/net v0.58.0\n'; } >"$tmp/xray-patch-sets-xnet-twice/patches/xray-x-net-0.60.0.patch"
check xray-patch-sets-xnet-twice 1 "patches/xray-x-net-0.60.0.patch sets golang.org/x/net in go.mod more than once"

fixture stage-lowercase-as
edit Dockerfile 's/ AS xray-builder$/ as xray-builder/'
gomod_patch v0.58.0 v0.59.0 >"$tmp/stage-lowercase-as/patches/xray-x-net-0.60.0.patch"
check stage-lowercase-as 1 "the Xray binary (Dockerfile patch order) uses golang.org/x/net v0.59.0"

fixture stage-mixed-case-name
edit Dockerfile 's/ AS xray-builder$/ As Xray-Builder/'
gomod_patch v0.58.0 v0.59.0 >"$tmp/stage-mixed-case-name/patches/xray-x-net-0.60.0.patch"
check stage-mixed-case-name 1 "the Xray binary (Dockerfile patch order) uses golang.org/x/net v0.59.0"

fixture stage-renamed
edit Dockerfile 's/ AS xray-builder$/ AS core-builder/'
check stage-renamed 1 "the Dockerfile has no xray-builder stage"

fixture stage-renamed-glob-still-checked
edit Dockerfile 's/ AS xray-builder$/ AS core-builder/'
gomod_patch v0.58.0 v0.59.0 >"$tmp/stage-renamed-glob-still-checked/patches/xray-x-net-0.60.0.patch"
check stage-renamed-glob-still-checked 1 "the Xray binary (make build_xray patch order) uses golang.org/x/net v0.59.0"

fixture apply-in-other-stage-not-counted
edit Dockerfile 's@^RUN make build$@RUN git apply --verbose /patches/other-xnet.patch \&\& make build@'
gomod_patch v0.58.0 v0.60.0 >"$tmp/apply-in-other-stage-not-counted/patches/other-xnet.patch"
printf 'Other\n\ndiff --git a/app/y.go b/app/y.go\n--- a/app/y.go\n+++ b/app/y.go\n@@ -1 +1 @@\n-a\n+b\n' >"$tmp/apply-in-other-stage-not-counted/patches/xray-grpc-1.83.2.patch"
cp "$tmp/apply-in-other-stage-not-counted/patches/xray-grpc-1.83.2.patch" "$tmp/apply-in-other-stage-not-counted/patches/xray-x-net-0.60.0.patch"
edit Dockerfile '/grep -q "golang.org\/x\/net /d'
check apply-in-other-stage-not-counted 1 "neither a patch applied by the Dockerfile xray-builder stage nor a go.mod check in it sets golang.org/x/net"

fixture apply-after-stage-ignored
printf 'FROM alpine:3.24.2@%s AS later\nRUN git apply --verbose /patches/other-lower.patch\n' "$alpine" >>"$tmp/apply-after-stage-ignored/Dockerfile"
gomod_patch v0.60.0 v0.58.0 >"$tmp/apply-after-stage-ignored/patches/other-lower.patch"
check apply-after-stage-ignored 0 "11 pins checked"

upstream_fixture() {
  fixture "$1"
  rm "$r/patches/xray-grpc-1.83.2.patch" "$r/patches/xray-x-net-0.60.0.patch"
  cat >"$r/Dockerfile" <<EOF
ARG XRAY_VERSION=v26.10.31

FROM --platform=\$BUILDPLATFORM golang:1.26.10-alpine@$digest AS builder
RUN make build

FROM --platform=\$BUILDPLATFORM golang:1.26.10-alpine@$digest AS singbox-builder
RUN go build ./cmd/sing-box

FROM --platform=\$BUILDPLATFORM golang:1.26.10-alpine@$digest AS xray-builder
ARG XRAY_COMMIT=556767e40baa142641838158d6cba743c879e5c3
COPY patches/xray-session-revoke.patch /patches/xray-session-revoke.patch
RUN git init -q . \\
 && git fetch -q --depth 1 origin \${XRAY_COMMIT} \\
 && test "\$(git rev-parse HEAD)" = "\${XRAY_COMMIT}" \\
 && grep -q "golang.org/x/net v0.62.0$" go.mod \\
 && git apply --verbose /patches/xray-session-revoke.patch \\
 && grep -qF "if a && b; then" app/x.go \\
 && grep -q "golang.org/x/net v0.62.0$" go.mod
RUN go build -o /out/xray ./main

FROM alpine:3.24.2@$alpine
COPY --from=builder /src/main /usr/local/bin/main
EOF
  printf "XRAY_COMMIT ?= \$(shell sed -n 's/^ARG XRAY_COMMIT=//p' Dockerfile)\n" >"$r/Makefile"
}

upstream_fixture upstream-gomod-meets-floor
check upstream-gomod-meets-floor 0 "11 pins checked, all at or above Go 1.26.10 and golang.org/x/net v0.60.0"

upstream_fixture upstream-gomod-meets-floor-indirect
edit Dockerfile 's/golang.org\/x\/net v0.62.0\$" go.mod/golang.org\/x\/net v0.62.0 \/\/ indirect$" go.mod/g'
check upstream-gomod-meets-floor-indirect 0 "11 pins checked"

upstream_fixture upstream-gomod-below-floor
edit Dockerfile 's/golang.org\/x\/net v0.62.0/golang.org\/x\/net v0.59.0/g'
check upstream-gomod-below-floor 1 "the Xray binary (Dockerfile xray-builder go.mod check) uses golang.org/x/net v0.59.0, below the security floor v0.60.0"

upstream_fixture upstream-gomod-below-floor-make
edit Dockerfile 's/golang.org\/x\/net v0.62.0/golang.org\/x\/net v0.59.0/g'
check upstream-gomod-below-floor-make 1 "the Xray binary (make build_xray, upstream go.mod at the Dockerfile XRAY_COMMIT) uses golang.org/x/net v0.59.0"

upstream_fixture upstream-gomod-pseudo-version
edit Dockerfile 's/golang.org\/x\/net v0.62.0/golang.org\/x\/net v0.62.1-0.20261001000000-0123456789ab/g'
check upstream-gomod-pseudo-version 1 "which is not a tagged release"

upstream_fixture upstream-neither-patch-nor-gomod
edit Dockerfile '/grep -q "golang.org\/x\/net /d'
check upstream-neither-patch-nor-gomod 1 "neither a patch applied by the Dockerfile xray-builder stage nor a go.mod check in it sets golang.org/x/net"

upstream_fixture upstream-neither-make
edit Dockerfile '/grep -q "golang.org\/x\/net /d'
check upstream-neither-make 1 "no patches/xray-*.patch sets golang.org/x/net and make build_xray has no upstream go.mod value"

upstream_fixture upstream-check-unanchored
edit Dockerfile 's/golang.org\/x\/net v0.62.0\$" go.mod/golang.org\/x\/net v0.62.0" go.mod/g'
check upstream-check-unanchored 1 "neither a patch applied by the Dockerfile xray-builder stage nor a go.mod check in it sets golang.org/x/net"

upstream_fixture upstream-check-negated
edit Dockerfile 's/&& grep -q "golang.org\/x\/net/\&\& ! grep -q "golang.org\/x\/net/g'
check upstream-check-negated 1 "neither a patch applied by the Dockerfile xray-builder stage nor a go.mod check in it sets golang.org/x/net"

upstream_fixture upstream-check-on-go-sum
edit Dockerfile 's/(golang.org\/x\/net v0.62.0\$") go.mod/\1 go.sum/g'
check upstream-check-on-go-sum 1 "neither a patch applied by the Dockerfile xray-builder stage nor a go.mod check in it sets golang.org/x/net"

upstream_fixture upstream-check-fixed-string
edit Dockerfile 's/grep -q "golang.org\/x\/net/grep -qF "golang.org\/x\/net/g'
check upstream-check-fixed-string 1 "neither a patch applied by the Dockerfile xray-builder stage nor a go.mod check in it sets golang.org/x/net"

upstream_fixture upstream-check-swallowed-by-or
edit Dockerfile 's/^ && grep -q "golang.org\/x\/net v0.62.0\$" go.mod$/ \&\& grep -q "golang.org\/x\/net v0.62.0$" go.mod || true/'
edit Dockerfile 's/^ && grep -q "golang.org\/x\/net v0.62.0\$" go.mod \\$/ \&\& grep -q "golang.org\/x\/net v0.62.0$" go.mod || true \\/'
check upstream-check-swallowed-by-or 1 "neither a patch applied by the Dockerfile xray-builder stage nor a go.mod check in it sets golang.org/x/net"

upstream_fixture upstream-check-swallowed-by-semicolon
edit Dockerfile 's/^ && grep -qF "if a && b; then" app\/x.go \\$/ \&\& grep -qF "if a \&\& b; then" app\/x.go; true \\/'
check upstream-check-swallowed-by-semicolon 1 "neither a patch applied by the Dockerfile xray-builder stage nor a go.mod check in it sets golang.org/x/net"

upstream_fixture upstream-check-in-command-substitution
edit Dockerfile 's/^ && grep -q "golang.org\/x\/net v0.62.0\$" go.mod$/ \&\& echo $(true \&\& grep -q "golang.org\/x\/net v0.62.0$" go.mod \&\& true)/'
edit Dockerfile 's/^ && grep -q "golang.org\/x\/net v0.62.0\$" go.mod \\$/ \&\& echo $(true \&\& grep -q "golang.org\/x\/net v0.62.0$" go.mod \&\& true) \\/'
check upstream-check-in-command-substitution 1 "neither a patch applied by the Dockerfile xray-builder stage nor a go.mod check in it sets golang.org/x/net"

upstream_fixture upstream-check-in-backquotes
edit Dockerfile 's/^ && grep -q "golang.org\/x\/net v0.62.0\$" go.mod$/ \&\& echo `true \&\& grep -q "golang.org\/x\/net v0.62.0$" go.mod \&\& true`/'
edit Dockerfile 's/^ && grep -q "golang.org\/x\/net v0.62.0\$" go.mod \\$/ \&\& echo `true \&\& grep -q "golang.org\/x\/net v0.62.0$" go.mod \&\& true` \\/'
check upstream-check-in-backquotes 1 "neither a patch applied by the Dockerfile xray-builder stage nor a go.mod check in it sets golang.org/x/net"

upstream_fixture upstream-check-in-subshell-list
edit Dockerfile 's/^ && grep -q "golang.org\/x\/net v0.62.0\$" go.mod$/ \&\& (true \&\& grep -q "golang.org\/x\/net v0.62.0$" go.mod \&\& true)/'
edit Dockerfile 's/^ && grep -q "golang.org\/x\/net v0.62.0\$" go.mod \\$/ \&\& (true \&\& grep -q "golang.org\/x\/net v0.62.0$" go.mod \&\& true) \\/'
check upstream-check-in-subshell-list 1 "neither a patch applied by the Dockerfile xray-builder stage nor a go.mod check in it sets golang.org/x/net"

upstream_fixture upstream-check-in-quoted-command-substitution
edit Dockerfile 's/^ && grep -q "golang.org\/x\/net v0.62.0\$" go.mod$/ \&\& echo "$(echo " \&\& grep -q "golang.org\/x\/net v0.62.0$" go.mod \&\& echo ")"/'
edit Dockerfile 's/^ && grep -q "golang.org\/x\/net v0.62.0\$" go.mod \\$/ \&\& echo "$(echo " \&\& grep -q "golang.org\/x\/net v0.62.0$" go.mod \&\& echo ")" \\/'
check upstream-check-in-quoted-command-substitution 1 "neither a patch applied by the Dockerfile xray-builder stage nor a go.mod check in it sets golang.org/x/net"

upstream_fixture upstream-check-in-quoted-backquotes
edit Dockerfile 's/^ && grep -q "golang.org\/x\/net v0.62.0\$" go.mod$/ \&\& echo "`echo " \&\& grep -q "golang.org\/x\/net v0.62.0$" go.mod \&\& echo "`"/'
edit Dockerfile 's/^ && grep -q "golang.org\/x\/net v0.62.0\$" go.mod \\$/ \&\& echo "`echo " \&\& grep -q "golang.org\/x\/net v0.62.0$" go.mod \&\& echo "`" \\/'
check upstream-check-in-quoted-backquotes 1 "neither a patch applied by the Dockerfile xray-builder stage nor a go.mod check in it sets golang.org/x/net"

upstream_fixture upstream-quoted-substitution-with-inner-quotes
edit Dockerfile 's/^ && git apply --verbose \/patches\/xray-session-revoke.patch \\$/ \&\& test "$(grep -c "golang.org\/x\/net v" go.mod)" = "1" \&\& git apply --verbose \/patches\/xray-session-revoke.patch \\/'
check upstream-quoted-substitution-with-inner-quotes 0 "11 pins checked"

upstream_fixture upstream-later-go-get-same-run
edit Dockerfile 's/^ && grep -q "golang.org\/x\/net v0.62.0\$" go.mod$/ \&\& grep -q "golang.org\/x\/net v0.62.0$" go.mod \&\& go get golang.org\/x\/net@v0.50.0/'
check upstream-later-go-get-same-run 1 "which can change the golang.org/x/net requirement in go.mod"

upstream_fixture upstream-later-go-mod-edit-separate-run
edit Dockerfile 's@^RUN go build -o /out/xray ./main$@RUN go mod edit -require=golang.org/x/net\@v0.50.0\nRUN go build -o /out/xray ./main@'
check upstream-later-go-mod-edit-separate-run 1 "which can change the golang.org/x/net requirement in go.mod"

upstream_fixture upstream-later-go-mod-tidy-separate-run
edit Dockerfile 's@^RUN go build -o /out/xray ./main$@RUN go mod tidy \&\& go build -o /out/xray ./main@'
check upstream-later-go-mod-tidy-separate-run 1 "which can change the golang.org/x/net requirement in go.mod"

upstream_fixture upstream-later-go-get-exec-form
edit Dockerfile 's@^RUN go build -o /out/xray ./main$@RUN ["go", "get", "golang.org/x/net\@v0.50.0"]\nRUN go build -o /out/xray ./main@'
check upstream-later-go-get-exec-form 1 "which can change the golang.org/x/net requirement in go.mod"

upstream_fixture upstream-later-go-get-heredoc
edit Dockerfile 's@^RUN go build -o /out/xray ./main$@RUN <<EOS\ngo get golang.org/x/net\@v0.50.0\nEOS\nRUN go build -o /out/xray ./main@'
check upstream-later-go-get-heredoc 1 "a heredoc RUN"

upstream_fixture upstream-later-go-get-quoted-command
edit Dockerfile 's@^RUN go build -o /out/xray ./main$@RUN "go" get golang.org/x/net\@v0.50.0\nRUN go${X} get golang.org/x/net\@v0.50.0\nRUN sh -c "g""o get golang.org/x/n""et\@v0.50.0"\nRUN go build -o /out/xray ./main@'
check upstream-later-go-get-quoted-command 1 "which can change the golang.org/x/net requirement in go.mod outside its go.mod check"

upstream_fixture upstream-check-after-cd
edit Dockerfile 's/^ && grep -q "golang.org\/x\/net v0.62.0\$" go.mod$/ \&\& cd \/tmp \&\& grep -q "golang.org\/x\/net v0.62.0$" go.mod \&\& cd \/xray-src/'
check upstream-check-after-cd 1 "so the go.mod check need not read the go.mod the build uses"

upstream_fixture upstream-check-after-cd-and-write
edit Dockerfile 's/^ && grep -q "golang.org\/x\/net v0.62.0\$" go.mod$/ \&\& cd \/tmp \&\& printf x > go.mod \&\& grep -q "golang.org\/x\/net v0.62.0$" go.mod \&\& cd \/xray-src/'
check upstream-check-after-cd-and-write 1 "so the go.mod check need not read the go.mod the build uses"

upstream_fixture upstream-write-before-check
edit Dockerfile 's/^ && grep -q "golang.org\/x\/net v0.62.0\$" go.mod$/ \&\& printf "golang.org\/x\/net v0.62.0\\n" >> go.mod \&\& grep -q "golang.org\/x\/net v0.62.0$" go.mod/'
check upstream-write-before-check 1 "which writes go.mod"

upstream_fixture upstream-sed-before-check
edit Dockerfile 's/^ && grep -q "golang.org\/x\/net v0.62.0\$" go.mod$/ \&\& sed -i "s\/v0.50.0\/v0.62.0\/" go.mod \&\& grep -q "golang.org\/x\/net v0.62.0$" go.mod/'
check upstream-sed-before-check 1 "which writes go.mod"

upstream_fixture upstream-copy-go-mod
edit Dockerfile 's@^COPY patches/xray-session-revoke.patch /patches/xray-session-revoke.patch$@COPY patches/xray-session-revoke.patch /patches/xray-session-revoke.patch\nCOPY other.mod go.mod@'
check upstream-copy-go-mod 1 "which writes go.mod"

upstream_fixture upstream-build-go-C
edit Dockerfile 's@^RUN go build -o /out/xray ./main$@RUN go -C /other build -o /out/xray ./main@'
check upstream-build-go-C 1 "so the go.mod check need not read the go.mod the build uses"

upstream_fixture upstream-build-modfile
edit Dockerfile 's@^RUN go build -o /out/xray ./main$@RUN go build -modfile=/other/go.mod -o /out/xray ./main@'
check upstream-build-modfile 1 "so the go.mod check need not read the go.mod the build uses"

upstream_fixture upstream-env-goflags
edit Dockerfile 's@^RUN go build -o /out/xray ./main$@ENV GOFLAGS=-modfile=/other/go.mod\nRUN go build -o /out/xray ./main@'
check upstream-env-goflags 1 "so the go.mod check need not read the go.mod the build uses"

upstream_fixture upstream-workdir-after-check
edit Dockerfile 's@^RUN go build -o /out/xray ./main$@WORKDIR /other\nRUN go build -o /out/xray ./main@'
check upstream-workdir-after-check 1 "so the go.mod check need not read the go.mod the build uses"

upstream_fixture upstream-workdir-changed-before-check
edit Dockerfile 's@^COPY patches/xray-session-revoke.patch /patches/xray-session-revoke.patch$@WORKDIR /xray-src\nRUN git init -q .\nWORKDIR /other\nCOPY patches/xray-session-revoke.patch /patches/xray-session-revoke.patch@'
check upstream-workdir-changed-before-check 1 "so the go.mod check need not read the go.mod the build uses"

upstream_fixture upstream-one-workdir-before-check
edit Dockerfile 's@^COPY patches/xray-session-revoke.patch /patches/xray-session-revoke.patch$@WORKDIR /xray-src\nCOPY patches/xray-session-revoke.patch /patches/xray-session-revoke.patch@'
check upstream-one-workdir-before-check 0 "11 pins checked"

upstream_fixture upstream-shell-instruction
edit Dockerfile 's@^RUN go build -o /out/xray ./main$@SHELL ["/bin/sh", "-c", "cd /other \&\& sh -c \\"$0\\""]\nRUN go build -o /out/xray ./main@'
check upstream-shell-instruction 1 "so the go.mod check need not read the go.mod the build uses"

upstream_fixture upstream-git-checkout-after-check
edit Dockerfile 's@^RUN go build -o /out/xray ./main$@RUN git checkout -q FETCH_HEAD~1 \&\& go build -o /out/xray ./main@'
check upstream-git-checkout-after-check 1 "after its golang.org/x/net go.mod check"

upstream_fixture upstream-check-in-other-stage
edit Dockerfile '/grep -q "golang.org\/x\/net /d'
edit Dockerfile 's@^RUN make build$@RUN grep -q "golang.org/x/net v0.62.0$" go.mod \&\& make build@'
check upstream-check-in-other-stage 1 "neither a patch applied by the Dockerfile xray-builder stage nor a go.mod check in it sets golang.org/x/net"

upstream_fixture upstream-check-exec-form
edit Dockerfile 's@^RUN go build -o /out/xray ./main$@RUN ["sh", "-c", "grep -q \\"golang.org/x/net v0.62.0$\\" go.mod"]@'
edit Dockerfile '/^ && grep -q "golang.org\/x\/net /d'
edit Dockerfile 's/^ && grep -qF "if a && b; then" app\/x.go \\$/ \&\& grep -qF "if a \&\& b; then" app\/x.go/'
check upstream-check-exec-form 1 "neither a patch applied by the Dockerfile xray-builder stage nor a go.mod check in it sets golang.org/x/net"

upstream_fixture upstream-later-patch-lowers
gomod_patch v0.62.0 v0.58.0 >"$tmp/upstream-later-patch-lowers/patches/xray-zz-lower.patch"
edit Dockerfile 's@^ && git apply --verbose /patches/xray-session-revoke.patch \\$@ \&\& git apply --verbose /patches/xray-session-revoke.patch \&\& git apply --verbose /patches/xray-zz-lower.patch \\@'
edit Dockerfile 's/^ && grep -q "golang.org\/x\/net v0.62.0\$" go.mod$/ \&\& grep -q "golang.org\/x\/net v0.58.0$" go.mod/'
check upstream-later-patch-lowers 1 "the Xray binary (Dockerfile patch order) uses golang.org/x/net v0.58.0"

upstream_fixture upstream-later-patch-lowers-make
gomod_patch v0.62.0 v0.58.0 >"$tmp/upstream-later-patch-lowers-make/patches/xray-zz-lower.patch"
check upstream-later-patch-lowers-make 1 "the Xray binary (make build_xray patch order) uses golang.org/x/net v0.58.0"

upstream_fixture upstream-later-patch-raises
gomod_patch v0.59.0 v0.61.0 >"$tmp/upstream-later-patch-raises/patches/xray-zz-raise.patch"
edit Dockerfile 's/^ && grep -q "golang.org\/x\/net v0.62.0\$" go.mod \\$/ \&\& grep -q "golang.org\/x\/net v0.59.0$" go.mod \\/'
edit Dockerfile 's@^ && git apply --verbose /patches/xray-session-revoke.patch \\$@ \&\& git apply --verbose /patches/xray-session-revoke.patch \&\& git apply --verbose /patches/xray-zz-raise.patch \\@'
edit Dockerfile 's/^ && grep -q "golang.org\/x\/net v0.62.0\$" go.mod$/ \&\& grep -q "golang.org\/x\/net v0.61.0$" go.mod/'
check upstream-later-patch-raises 0 "11 pins checked"

upstream_fixture upstream-check-contradicts-patch-order
edit Dockerfile 's/^ && grep -q "golang.org\/x\/net v0.62.0\$" go.mod$/ \&\& grep -q "golang.org\/x\/net v0.63.0$" go.mod/'
check upstream-check-contradicts-patch-order 1 "the Dockerfile xray-builder stage checks go.mod for golang.org/x/net v0.63.0, but its go.mod check leaves v0.62.0 there"

upstream_fixture upstream-make-commit-not-tied
printf 'XRAY_COMMIT ?= 0123456789abcdef0123456789abcdef01234567\n' >"$tmp/upstream-make-commit-not-tied/Makefile"
check upstream-make-commit-not-tied 1 "no patches/xray-*.patch sets golang.org/x/net and make build_xray has no upstream go.mod value"

upstream_fixture upstream-make-commit-overridden
printf 'XRAY_COMMIT := 0123456789abcdef0123456789abcdef01234567\n' >>"$tmp/upstream-make-commit-overridden/Makefile"
check upstream-make-commit-overridden 1 "make build_xray has no upstream go.mod value"

upstream_fixture upstream-two-commit-args
printf 'ARG XRAY_COMMIT=0123456789abcdef0123456789abcdef01234567\n' >>"$tmp/upstream-two-commit-args/Dockerfile"
check upstream-two-commit-args 1 "make build_xray has no upstream go.mod value"

upstream_fixture upstream-no-makefile
rm "$tmp/upstream-no-makefile/Makefile"
check upstream-no-makefile 1 "make build_xray has no upstream go.mod value"

fixture gotoolchain-env-version
edit Dockerfile 's@^RUN go build -o /out/xray ./main$@ENV GOTOOLCHAIN=go1.27.0\nRUN go build -o /out/xray ./main@'
check gotoolchain-env-version 1 "which sets GOTOOLCHAIN to something other than local or sets GOENV"

fixture gotoolchain-env-legacy-form
edit Dockerfile 's@^RUN go build -o /out/xray ./main$@ENV GOTOOLCHAIN go1.27.0+auto\nRUN go build -o /out/xray ./main@'
check gotoolchain-env-legacy-form 1 "which sets GOTOOLCHAIN to something other than local or sets GOENV"

fixture gotoolchain-arg
edit Dockerfile 's@^RUN go build -o /out/xray ./main$@ARG GOTOOLCHAIN=go1.27.0\nRUN go build -o /out/xray ./main@'
check gotoolchain-arg 1 "which sets GOTOOLCHAIN to something other than local or sets GOENV"

fixture gotoolchain-inline-run
edit Dockerfile 's@^RUN go build -o /out/xray ./main$@RUN GOTOOLCHAIN=go1.27.0 go build -o /out/xray ./main@'
check gotoolchain-inline-run 1 "which sets GOTOOLCHAIN to something other than local or sets GOENV"

fixture gotoolchain-export
edit Dockerfile 's@^RUN go build -o /out/xray ./main$@RUN export GOTOOLCHAIN=go1.27.0 \&\& go build -o /out/xray ./main@'
check gotoolchain-export 1 "which sets GOTOOLCHAIN to something other than local or sets GOENV"

fixture gotoolchain-go-env-w
edit Dockerfile 's@^RUN go build -o /out/xray ./main$@RUN go env -w GOTOOLCHAIN=go1.27.0\nRUN go build -o /out/xray ./main@'
check gotoolchain-go-env-w 1 "which sets GOTOOLCHAIN to something other than local or sets GOENV"

fixture gotoolchain-quoted-name
edit Dockerfile 's@^RUN go build -o /out/xray ./main$@RUN GOTOOL""CHAIN=go1.27.0 go build -o /out/xray ./main@'
check gotoolchain-quoted-name 1 "which sets GOTOOLCHAIN to something other than local or sets GOENV"

fixture goenv-env
edit Dockerfile 's@^RUN go build -o /out/xray ./main$@ENV GOENV=/tmp/goenv\nRUN go build -o /out/xray ./main@'
check goenv-env 1 "which sets GOTOOLCHAIN to something other than local or sets GOENV"

fixture gotoolchain-local-accepted
edit Dockerfile 's@^RUN go build -o /out/xray ./main$@ENV GOTOOLCHAIN=local\nRUN go build -o /out/xray ./main@'
check gotoolchain-local-accepted 0 "11 pins checked"

fixture stage-xray-builder-twice
printf "\nFROM --platform=\$BUILDPLATFORM golang:1.26.10-alpine@%s AS Xray-Builder\nRUN go build -o /out/xray ./main\n" "$digest" >>"$tmp/stage-xray-builder-twice/Dockerfile"
check stage-xray-builder-twice 1 "the Dockerfile has 2 xray-builder stages, so the one the image copies from cannot be told apart"

fixture xray-copied-from-other-stage
printf 'COPY --from=builder /out/xray /usr/local/bin/xray\n' >>"$tmp/xray-copied-from-other-stage/Dockerfile"
check xray-copied-from-other-stage 1 "the Dockerfile copies /usr/local/bin/xray from 'builder', not from the xray-builder stage"

fixture xray-copied-from-xray-builder
printf 'COPY --from=xray-builder /out/xray /usr/local/bin/xray\n' >>"$tmp/xray-copied-from-xray-builder/Dockerfile"
check xray-copied-from-xray-builder 0 "11 pins checked"

inject_before() {
  upstream_fixture "$1"
  printf '%s\n' "$3" >"$tmp/$1.inject"
  awk -v f="$tmp/$1.inject" -v at="$2" '$0 == at { while ((getline l < f) > 0) print l } { print }' "$r/Dockerfile" >"$r/Dockerfile.new"
  mv "$r/Dockerfile.new" "$r/Dockerfile"
}

later_run() {
  inject_before "$1" "RUN go build -o /out/xray ./main" "$2"
}

not_allowed="is not grep, test, git apply of a /patches/ patch or go build, after its golang.org/x/net go.mod check"
while IFS='|' read -r name line; do
  later_run "$name" "$line"
  check "$name" 1 "$not_allowed"
done <<'EOF'
later-unbraced-parameter|RUN go$X get golang.org/x/net@v0.50.0
later-sh-c-positional|RUN sh -c 'go$1 get golang.org/x/net@v0.50.0'
later-cd-unbraced|RUN cd$X /tmp
later-default-value|RUN g${X:-o} get golang.org/x/net@v0.50.0
later-exec-default-value|RUN ["sh","-c","g${X:-o} get golang.org/x/net@v0.50.0"]
later-nested-braces|RUN go${X:-${Y}} get golang.org/x/net@v0.50.0
later-ansi-c-hex|RUN $'\x67o' get golang.org/x/net@v0.50.0
later-ansi-c-octal|RUN $'\147o' get golang.org/x/net@v0.50.0
later-empty-substitution|RUN go$(true) get golang.org/x/net@v0.50.0
later-substituted-command|RUN $(echo go) get golang.org/x/net@v0.50.0
later-backquoted-command|RUN `echo go` get golang.org/x/net@v0.50.0
later-variable-command|RUN G=go; $G get golang.org/x/net@v0.50.0
later-variable-subcommand|RUN G=get; go $G golang.org/x/net@v0.50.0
later-symlinked-go|RUN ln -s /usr/local/go/bin/go /usr/local/bin/g && g get golang.org/x/net@v0.50.0
later-patch-script|RUN sh /patches/x.sh
later-pipeline-part|RUN grep -c golang go.sum | g get golang.org/x/net@v0.50.0
later-inside-substitution|RUN test "$(G=go; $G get golang.org/x/net@v0.50.0)" = ""
later-inside-quoted-pipe|RUN grep -q "a|b" go.sum | g get golang.org/x/net@v0.50.0
later-git-apply-elsewhere|RUN git apply /tmp/x.patch
later-go-env-write|RUN go env -w GOFLAGS=-mod=mod
EOF

later_run later-continuation-joined-without-space 'RUN go g\
et golang.org/x/net@v0.50.0'
check later-continuation-joined-without-space 1 "$not_allowed"

inject_before early-go-env-variable-name 'RUN git init -q . \' 'RUN X=TOOLCHAIN; go env -w GO$X=go1.27.0'
check early-go-env-variable-name 1 "has a \$ or a backquote in a command name or in what it exports"

inject_before early-export-variable-name 'RUN git init -q . \' 'RUN export GO$X=go1.27.0 && go build -o /out/xray ./main'
check early-export-variable-name 1 "has a \$ or a backquote in a command name or in what it exports"

inject_before early-command-from-variable 'RUN git init -q . \' 'RUN $G version'
check early-command-from-variable 1 "has a \$ or a backquote in a command name or in what it exports"

inject_before env-name-split-by-continuation 'RUN git init -q . \' 'ENV GO\
TOOLCHAIN=go1.27.0'
check env-name-split-by-continuation 1 "which sets GOTOOLCHAIN to something other than local or sets GOENV"

later_run later-allowed-shapes 'RUN test "$(grep -cF '"'"'a|b'"'"' go.mod)" = "1" \
 && ! (grep -hE "(^|x)r[.](a|b)" go.mod | grep -qvE "r[.](a|b)[.](Load|Store)[(]") \
 && grep -A1 -F "a" go.mod | grep -qF "b" \
 && git apply --verbose /patches/xray-session-revoke.patch
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -trimpath \
    -ldflags "-s -w -buildid=" \
    -o /out/xray2 ./main'
check later-allowed-shapes 0 "pins checked"

fixture xray-copied-json-form
printf 'COPY --from=builder ["/out/xray", "/usr/local/bin/xray"]\n' >>"$tmp/xray-copied-json-form/Dockerfile"
check xray-copied-json-form 1 "the Dockerfile copies /usr/local/bin/xray from 'builder', not from the xray-builder stage"

fixture xray-copied-double-slash
printf 'COPY --from=builder /out/xray /usr/local/bin//xray\n' >>"$tmp/xray-copied-double-slash/Dockerfile"
check xray-copied-double-slash 1 "the Dockerfile copies /usr/local/bin/xray from 'builder', not from the xray-builder stage"

fixture xray-copied-dot-segment
printf 'COPY --from=builder /out/xray /usr/local/./bin/xray\n' >>"$tmp/xray-copied-dot-segment/Dockerfile"
check xray-copied-dot-segment 1 "the Dockerfile copies /usr/local/bin/xray from 'builder', not from the xray-builder stage"

fixture xray-copied-json-from-xray-builder
printf 'COPY --from=xray-builder ["/out/xray", "/usr/local/bin/xray"]\n' >>"$tmp/xray-copied-json-from-xray-builder/Dockerfile"
check xray-copied-json-from-xray-builder 0 "11 pins checked"

if [ -f "$repo_root/Dockerfile" ] && [ -f "$repo_root/Makefile" ]; then
  set +e
  bash "$guard" "$repo_root" >"$tmp/repo-tree.out" 2>&1
  got=$?
  set -e
  if [ "$got" = 0 ]; then
    printf 'ok       %-40s exit %s\n' repo-tree "$got"
    pass=$((pass + 1))
  else
    printf 'FAIL     %-40s exit %s, want 0\n' repo-tree "$got"
    sed 's/^/         | /' "$tmp/repo-tree.out" | tail -8
    failed=$((failed + 1))
  fi
fi

fixture base-tree-shape
edit go.mod 's/^toolchain go1.26.10$/toolchain go1.26.6/; s/golang.org\/x\/net v0.60.0/golang.org\/x\/net v0.58.0/'
edit .github/workflows/run-tests.yml 's/1.26.10/1.26.6/'
edit .github/workflows/publish-release.yml 's/1.26.10/1.26.6/'
edit Dockerfile 's/golang:1.26.10-alpine/golang:1.26.6-alpine/'
edit Dockerfile.wireguard 's/golang:1.26.10-alpine/golang:1.26.6-alpine/'
gomod_patch v0.58.0 v0.59.0 >"$tmp/base-tree-shape/patches/xray-x-net-0.60.0.patch"
check base-tree-shape 1 "12 problems in 12 pins"

printf 'test-toolchain-floor: %d passed, %d failed\n' "$pass" "$failed"
[ "$failed" = 0 ]
