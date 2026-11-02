#!/usr/bin/env bash
set -eu
set -o pipefail

repo_root=$(git rev-parse --show-toplevel)
gate=${GOVULNCHECK_GATE:-$repo_root/scripts/govulncheck_gate.py}
[ -f "$gate" ] || { printf 'test-govulncheck-gate: %s is missing\n' "$gate" >&2; exit 2; }
gate=$(cd "$(dirname "$gate")" && pwd)/$(basename "$gate")

tmp=$(mktemp -d "${TMPDIR:-/tmp}/govulncheck-gate-test.XXXXXX")
trap 'rm -rf "$tmp"' EXIT

pass=0
failed=0
osv=GO-2026-6651
module=github.com/xtls/xray-core
version=v1.260327.0
patch=xray-pinned-ca-servername.patch
waiver="$osv $module $version 2026-11-30 patches/$patch the shipped binary carries the pinned CA patch"

config_msg() {
  printf '{"config":{"protocol_version":"v1.0.0","scanner_name":"govulncheck","scanner_version":"v1.8.0","db":"https://vuln.go.dev","go_version":"go1.26.10","scan_level":"%s","scan_mode":"source"}}\n' "${1:-symbol}"
}

osv_msg() {
  printf '{"osv":{"id":"%s","summary":"summary of %s"}}\n' "$1" "$1"
}

called() {
  printf '{"finding":{"osv":"%s","fixed_version":"v9.9.9","trace":[{"module":"%s","version":"%s","package":"%s/app","function":"Do","receiver":"*Handler"},{"module":"github.com/pasarguard/node","package":"github.com/pasarguard/node/controller","function":"Run"}]}}\n' "$1" "$2" "$3" "$2"
}

imported() {
  printf '{"finding":{"osv":"%s","fixed_version":"v9.9.9","trace":[{"module":"%s","version":"%s","package":"%s/app"}]}}\n' "$1" "$2" "$3" "$2"
}

required() {
  printf '{"finding":{"osv":"%s","fixed_version":"v9.9.9","trace":[{"module":"%s","version":"%s"}]}}\n' "$1" "$2" "$3"
}

fixture() {
  r=$tmp/$1
  mkdir -p "$r/patches"
  cat >"$r/Dockerfile" <<EOF
FROM golang:1.26.10-alpine AS xray-builder
COPY patches/xray-http-100-continue.patch /patches/xray-http-100-continue.patch
COPY patches/$patch /patches/$patch
RUN git init -q . \\
 && git apply --verbose /patches/xray-http-100-continue.patch \\
 && git apply --verbose /patches/$patch \\
 && grep -qF 'Pinning CA needs a valid ServerName' transport/internet/tls/config.go
EOF
  printf 'diff --git a/x.go b/x.go\n' >"$r/patches/$patch"
  printf 'diff --git a/y.go b/y.go\n' >"$r/patches/xray-http-100-continue.patch"
  printf '%s\n' "$waiver" >"$r/waivers.txt"
  {
    config_msg
    printf '{\n  "progress": {\n    "message": "Scanning your code and 2 packages across 1 dependent module for known vulnerabilities..."\n  }\n}\n'
    printf '{\n  "SBOM": {\n    "go_version": "go1.26.10",\n    "modules": [\n      {\n        "path": "github.com/pasarguard/node"\n      }\n    ]\n  }\n}\n'
    osv_msg "$osv"
    osv_msg GO-2026-6860
    required "$osv" "$module" "$version"
    imported "$osv" "$module" "$version"
    called "$osv" "$module" "$version"
    called "$osv" "$module" "$version"
    required GO-2026-6860 golang.org/x/net v0.60.0
  } >"$r/stream.json"
}

edit() {
  local file=$1 expr=$2
  sed -E "$expr" "$r/$file" >"$r/$file.new"
  if cmp -s "$r/$file" "$r/$file.new"; then
    printf 'FAIL     %-44s the fixture edit %s on %s changed nothing\n' "$(basename "$r")" "$expr" "$file"
    failed=$((failed + 1))
  fi
  mv "$r/$file.new" "$r/$file"
}

check() {
  name=$1 want=$2 want_text=${3:-} today=${4:-2026-11-02}
  r=$tmp/$name
  set +e
  GOVULN_GATE_TODAY=$today python3 "$gate" "$r/waivers.txt" "$r/stream.json" "$r" >"$tmp/$name.out" 2>&1
  got=$?
  set -e
  if [ "$got" = "$want" ] && { [ -z "$want_text" ] || grep -qF -- "$want_text" "$tmp/$name.out"; }; then
    printf 'ok       %-44s exit %s\n' "$name" "$got"
    pass=$((pass + 1))
  else
    printf 'FAIL     %-44s exit %s, want %s%s\n' "$name" "$got" "$want" "${want_text:+ and output \"$want_text\"}"
    sed 's/^/         | /' "$tmp/$name.out" | tail -8
    failed=$((failed + 1))
  fi
}

fixture waived-only
check waived-only 0 "WAIVED $osv $module@$version until 2026-11-30: the shipped binary carries the pinned CA patch (2 call paths)"

fixture waived-only-lists-uncalled
check waived-only-lists-uncalled 0 "INFO GO-2026-6860 golang.org/x/net@v0.60.0 is module-level only, not called"

fixture extra-called-finding
called GO-2026-7001 golang.org/x/net v0.60.0 >>"$tmp/extra-called-finding/stream.json"
check extra-called-finding 1 "FAIL GO-2026-7001 golang.org/x/net@v0.60.0 is called (1 call paths), no waiver"

fixture extra-called-stdlib-finding
called GO-2026-7002 stdlib v1.26.10 >>"$tmp/extra-called-stdlib-finding/stream.json"
check extra-called-stdlib-finding 1 "FAIL GO-2026-7002 stdlib@v1.26.10 is called"

fixture same-osv-other-version
edit stream.json "s/\"version\":\"$version\"/\"version\":\"v1.260327.1-0.20260710210335-64fada32b5b9\"/g"
check same-osv-other-version 1 "FAIL $osv $module@v1.260327.1-0.20260710210335-64fada32b5b9 is called (2 call paths), no waiver"

fixture same-osv-other-version-stale
edit stream.json "s/\"version\":\"$version\"/\"version\":\"v1.260327.1-0.20260710210335-64fada32b5b9\"/g"
check same-osv-other-version-stale 1 "matches no called vulnerability in this scan; a stale waiver must be removed"

fixture same-osv-other-module
called "$osv" github.com/xtls/reality v0.0.0-20260101000000-000000000000 >>"$tmp/same-osv-other-module/stream.json"
check same-osv-other-module 1 "FAIL $osv github.com/xtls/reality@v0.0.0-20260101000000-000000000000 is called"

fixture expired
check expired 1 "expired on 2026-11-30" 2026-12-01

fixture expired-finding-fails
check expired-finding-fails 1 "FAIL $osv $module@$version is called (2 call paths), its waiver on line 1 is not active" 2026-12-01

fixture expires-today
check expires-today 0 "WAIVED $osv" 2026-11-30

fixture expiry-beyond-horizon
edit waivers.txt 's/ 2026-11-30 / 2027-06-01 /'
check expiry-beyond-horizon 1 "expires 2027-06-01, more than 90 days after 2026-11-02"

fixture expired-unused-waiver
printf 'GO-2026-6000 golang.org/x/net v0.58.0 2026-10-01 patches/%s an old waiver nobody removed\n' "$patch" >>"$tmp/expired-unused-waiver/waivers.txt"
check expired-unused-waiver 1 "waiver line 2 (GO-2026-6000 golang.org/x/net@v0.58.0): expired on 2026-10-01"

fixture patch-missing
rm "$tmp/patch-missing/patches/$patch"
check patch-missing 1 "patches/$patch does not exist"

fixture patch-outside-repo
edit waivers.txt "s@ patches/$patch @ ../$patch @"
printf 'x\n' >"$tmp/$patch"
check patch-outside-repo 1 "../$patch is not a path inside the repository"

fixture patch-absolute
edit waivers.txt "s@ patches/$patch @ /etc/hosts @"
check patch-absolute 1 "/etc/hosts is not a path inside the repository"

fixture dockerfile-without-apply
edit Dockerfile "/git apply --verbose \/patches\/$patch/d"
check dockerfile-without-apply 1 "no 'git apply' line in the Dockerfile applies $patch"

fixture dockerfile-copy-only
edit Dockerfile "s@ && git apply --verbose /patches/$patch @ \&\& ls /patches/$patch @"
check dockerfile-copy-only 1 "no 'git apply' line in the Dockerfile applies $patch"

fixture dockerfile-apply-commented-out
edit Dockerfile "s@^ && git apply --verbose /patches/$patch \\\\\$@# \&\& git apply --verbose /patches/$patch \\\\@"
check dockerfile-apply-commented-out 1 "no 'git apply' line in the Dockerfile applies $patch"

fixture dockerfile-applies-lookalike
edit Dockerfile "s@git apply --verbose /patches/$patch @git apply --verbose /patches/$patch.orig @"
check dockerfile-applies-lookalike 1 "no 'git apply' line in the Dockerfile applies $patch"

fixture dockerfile-apply-then-semicolon
edit Dockerfile "s@git apply --verbose /patches/$patch \\\\\$@git apply --verbose /patches/$patch; true \\\\@"
check dockerfile-apply-then-semicolon 0 "WAIVED $osv"

fixture dockerfile-missing
rm "$tmp/dockerfile-missing/Dockerfile"
check dockerfile-missing 1 "cannot read the Dockerfile"

fixture stale-waiver
grep -v '"function"' "$tmp/stale-waiver/stream.json" >"$tmp/stale-waiver/stream.json.new"
mv "$tmp/stale-waiver/stream.json.new" "$tmp/stale-waiver/stream.json"
check stale-waiver 1 "($osv $module@$version): matches no called vulnerability in this scan; a stale waiver must be removed"

fixture stale-waiver-no-finding-at-all
grep -v "\"osv\":\"$osv\"" "$tmp/stale-waiver-no-finding-at-all/stream.json" >"$tmp/stale-waiver-no-finding-at-all/stream.json.new"
mv "$tmp/stale-waiver-no-finding-at-all/stream.json.new" "$tmp/stale-waiver-no-finding-at-all/stream.json"
check stale-waiver-no-finding-at-all 1 "a stale waiver must be removed"

fixture uncalled-only-without-waiver
: >"$tmp/uncalled-only-without-waiver/waivers.txt"
{ config_msg; imported GO-2026-7003 golang.org/x/crypto v0.57.0; required GO-2026-6860 golang.org/x/net v0.60.0; } >"$tmp/uncalled-only-without-waiver/stream.json"
check uncalled-only-without-waiver 0 "INFO GO-2026-7003 golang.org/x/crypto@v0.57.0 is package-level only, not called"

fixture clean-scan-empty-waivers
: >"$tmp/clean-scan-empty-waivers/waivers.txt"
config_msg >"$tmp/clean-scan-empty-waivers/stream.json"
check clean-scan-empty-waivers 0 "govulncheck-gate: pass: 0 called vulnerabilities"

fixture called-without-waiver-file-entry
: >"$tmp/called-without-waiver-file-entry/waivers.txt"
check called-without-waiver-file-entry 1 "FAIL $osv $module@$version is called (2 call paths), no waiver"

fixture no-config
grep -v '"config"' "$tmp/no-config/stream.json" >"$tmp/no-config/stream.json.new"
mv "$tmp/no-config/stream.json.new" "$tmp/no-config/stream.json"
check no-config 1 "has 0 config messages, want exactly 1"

fixture two-configs
config_msg >>"$tmp/two-configs/stream.json"
check two-configs 1 "has 2 config messages, want exactly 1"

fixture config-not-first
{ called "$osv" "$module" "$version"; config_msg; } >"$tmp/config-not-first/stream.json"
check config-not-first 1 "does not start with the config message"

fixture empty-stream
: >"$tmp/empty-stream/stream.json"
check empty-stream 1 "has 0 config messages"

fixture garbage-stream
printf 'panic: runtime error: invalid memory address\n' >"$tmp/garbage-stream/stream.json"
check garbage-stream 1 "is not a govulncheck JSON stream"

fixture truncated-stream
size=$(wc -c <"$tmp/truncated-stream/stream.json")
head -c "$((size - 40))" "$tmp/truncated-stream/stream.json" >"$tmp/truncated-stream/stream.json.new"
mv "$tmp/truncated-stream/stream.json.new" "$tmp/truncated-stream/stream.json"
check truncated-stream 1 "is not a govulncheck JSON stream"

fixture array-stream
{ printf '['; config_msg; printf ']\n'; } >"$tmp/array-stream/stream.json"
check array-stream 1 "is not a single-key govulncheck message object"

fixture unknown-message
printf '{"vulnerability":{"id":"%s"}}\n' "$osv" >>"$tmp/unknown-message/stream.json"
check unknown-message 1 "has the unknown type 'vulnerability'"

fixture finding-without-trace
printf '{"finding":{"osv":"GO-2026-7004","trace":[]}}\n' >>"$tmp/finding-without-trace/stream.json"
check finding-without-trace 1 "has no usable trace"

fixture module-scan-level
{ config_msg module; required "$osv" "$module" "$version"; } >"$tmp/module-scan-level/stream.json"
check module-scan-level 1 "scan_level is 'module', want 'symbol'"

fixture package-scan-level
{ config_msg package; imported GO-2026-7003 golang.org/x/crypto v0.57.0; } >"$tmp/package-scan-level/stream.json"
check package-scan-level 1 "scan_level is 'package', want 'symbol'"

fixture stream-missing
rm "$tmp/stream-missing/stream.json"
check stream-missing 1 "cannot read the govulncheck stream"

fixture waiver-file-missing
rm "$tmp/waiver-file-missing/waivers.txt"
check waiver-file-missing 1 "cannot read the waiver file"

fixture waiver-without-reason
printf '%s %s %s 2026-11-30 patches/%s\n' "$osv" "$module" "$version" "$patch" >"$tmp/waiver-without-reason/waivers.txt"
check waiver-without-reason 1 "waiver line 1 is not '<OSV id> <module> <version> <expires YYYY-MM-DD> <patch file> <reason>' with single spaces"

fixture waiver-tab-separated
printf '%s\t%s %s 2026-11-30 patches/%s reason\n' "$osv" "$module" "$version" "$patch" >"$tmp/waiver-tab-separated/waivers.txt"
check waiver-tab-separated 1 "waiver line 1 is not '<OSV id>"

fixture waiver-double-space
edit waivers.txt "s@^$osv $module@$osv  $module@"
check waiver-double-space 1 "waiver line 1 is not '<OSV id>"

fixture waiver-leading-space
edit waivers.txt "s/^/ /"
check waiver-leading-space 1 "waiver line 1 is not '<OSV id>"

fixture waiver-bad-id
edit waivers.txt "s/^$osv /CVE-2026-1234 /"
check waiver-bad-id 1 "'CVE-2026-1234' is not a GO-YYYY-NNNN id"

fixture waiver-floating-version
edit waivers.txt "s/ $version / latest /"
check waiver-floating-version 1 "'latest' is not an exact module version"

fixture waiver-impossible-date
edit waivers.txt 's/ 2026-11-30 / 2026-11-31 /'
check waiver-impossible-date 1 "the expiry '2026-11-31' is not a real calendar date"

fixture waiver-date-format
edit waivers.txt 's/ 2026-11-30 / 30-11-2026 /'
check waiver-date-format 1 "the expiry '30-11-2026' is not a YYYY-MM-DD date"

fixture waiver-duplicate-id
printf '%s\n' "$waiver" >>"$tmp/waiver-duplicate-id/waivers.txt"
check waiver-duplicate-id 1 "waiver line 2 repeats $osv, already waived on line 1"

fixture waiver-comments-and-blanks
printf '# reviewed 2026-11-02\n\n%s\n\n' "$waiver" >"$tmp/waiver-comments-and-blanks/waivers.txt"
check waiver-comments-and-blanks 0 "WAIVED $osv"

fixture today-override-invalid
check today-override-invalid 1 "GOVULN_GATE_TODAY 'yesterday' is not a YYYY-MM-DD date" yesterday

set +e
python3 "$gate" only-one-argument >"$tmp/usage.out" 2>&1
got=$?
set -e
if [ "$got" = 2 ] && grep -qF 'usage: python3 scripts/govulncheck_gate.py' "$tmp/usage.out"; then
  printf 'ok       %-44s exit %s\n' usage "$got"
  pass=$((pass + 1))
else
  printf 'FAIL     %-44s exit %s, want 2 and a usage line\n' usage "$got"
  failed=$((failed + 1))
fi

printf 'test-govulncheck-gate: %d passed, %d failed\n' "$pass" "$failed"
[ "$failed" = 0 ]
