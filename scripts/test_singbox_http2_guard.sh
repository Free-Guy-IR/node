#!/usr/bin/env bash
set -eu
set -o pipefail

repo_root=$(git rev-parse --show-toplevel)
guard=${SINGBOX_HTTP2_GUARD:-$repo_root/scripts/singbox_http2_guard.sh}
[ -f "$guard" ] || { printf 'test-singbox-http2-guard: %s is missing\n' "$guard" >&2; exit 2; }
guard=$(cd "$(dirname "$guard")" && pwd)/$(basename "$guard")

tmp=$(mktemp -d "${TMPDIR:-/tmp}/singbox-http2-guard-test.XXXXXX")
trap 'rm -rf "$tmp"' EXIT

export GOFLAGS=-mod=mod GOPROXY=off GOSUMDB=off GOWORK=off GOTOOLCHAIN=local

pass=0
failed=0

legacy='//go:build !(go1.27 && !http2legacy)'
wrap='//go:build go1.27 && !http2legacy'

source_file() {
  if [ -n "$1" ]; then
    printf '%s\n\n' "$1"
  fi
  printf 'package http2\n'
}

test_file() {
  printf '%s\n\npackage http2\n\nimport "testing"\n\n' "$legacy"
  local name
  for name in TestServerMaxHandlerGoroutines TestServerContinuationFlood TestServerContinuationAfterInvalidHeader; do
    case " $1 " in
      *" missing:$name "*) ;;
      *" fails:$name "*) printf 'func %s(t *testing.T) { t.Fatal("limit not enforced") }\n\n' "$name" ;;
      *) printf 'func %s(t *testing.T) {}\n\n' "$name" ;;
    esac
  done
}

fixture() {
  local name=$1 tests=$2 wrap_constraint=$3
  local root=$tmp/$name
  mkdir -p "$root/xnet/http2" "$root/module"
  printf 'module golang.org/x/net\n\ngo 1.26.0\n' > "$root/xnet/go.mod"
  printf 'module guardprobe\n\ngo 1.26.0\n\nrequire golang.org/x/net v0.60.0\n\nreplace golang.org/x/net => ../xnet\n' > "$root/module/go.mod"
  source_file "$legacy" > "$root/xnet/http2/server.go"
  source_file "$legacy" > "$root/xnet/http2/transport.go"
  source_file "" > "$root/xnet/http2/frame.go"
  source_file "$wrap_constraint" > "$root/xnet/http2/server_wrap.go"
  source_file "$wrap_constraint" > "$root/xnet/http2/transport_wrap.go"
  test_file "$tests" > "$root/xnet/http2/server_test.go"
}

check() {
  local name=$1 tags=$2 want=$3 needle=$4
  local out rc
  set +e
  out=$(cd "$tmp/$name/module" && sh "$guard" "$tags" 2>&1)
  rc=$?
  set -e
  if [ "$want" = 0 ] && [ "$rc" = 0 ]; then
    pass=$((pass + 1))
  elif [ "$want" = 1 ] && [ "$rc" != 0 ] && printf '%s' "$out" | grep -qF -- "$needle"; then
    pass=$((pass + 1))
  else
    failed=$((failed + 1))
    printf 'FAIL %s: tags %s, want rc %s with "%s", got rc %s:\n%s\n' "$name" "$tags" "$want" "$needle" "$rc" "$out" >&2
  fi
}

fixture hardened "" "$wrap"
check hardened with_quic,with_utls,with_clash_api,with_v2ray_api,http2legacy 0 ""
if (cd "$tmp/hardened/module" && go list -tags with_quic -f '{{join .GoFiles " "}}' golang.org/x/net/http2) | grep -qw server_wrap.go; then
  check hardened with_quic,with_utls,with_clash_api,with_v2ray_api 1 "does not compile http2/server.go"
else
  check hardened with_quic,with_utls,with_clash_api,with_v2ray_api 0 ""
fi

fixture rapid-reset-fails "fails:TestServerMaxHandlerGoroutines" "$wrap"
check rapid-reset-fails with_quic,http2legacy 1 "--- FAIL: TestServerMaxHandlerGoroutines"

fixture flood-fails "fails:TestServerContinuationFlood" "$wrap"
check flood-fails with_quic,http2legacy 1 "--- FAIL: TestServerContinuationFlood"

fixture invalid-fails "fails:TestServerContinuationAfterInvalidHeader" "$wrap"
check invalid-fails with_quic,http2legacy 1 "--- FAIL: TestServerContinuationAfterInvalidHeader"

fixture rapid-reset-missing "missing:TestServerMaxHandlerGoroutines" "$wrap"
check rapid-reset-missing with_quic,http2legacy 1 "did not run and pass TestServerMaxHandlerGoroutines"

fixture flood-missing "missing:TestServerContinuationFlood" "$wrap"
check flood-missing with_quic,http2legacy 1 "did not run and pass TestServerContinuationFlood"

fixture invalid-missing "missing:TestServerContinuationAfterInvalidHeader" "$wrap"
check invalid-missing with_quic,http2legacy 1 "did not run and pass TestServerContinuationAfterInvalidHeader"

fixture wrap-always "" '//go:build go1.20'
check wrap-always with_quic,http2legacy 1 "compiles http2/server_wrap.go"

rm "$tmp/hardened/xnet/http2/transport.go"
check hardened with_quic,http2legacy 1 "does not compile http2/transport.go"

printf 'test-singbox-http2-guard: %d passed, %d failed\n' "$pass" "$failed"
[ "$failed" = 0 ]
