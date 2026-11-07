#!/bin/sh
set -eu

tags=${1:?usage: singbox_http2_guard.sh <sing-box build tags>}

refuse() {
  printf 'singbox-http2-guard: %s\n' "$1" >&2
  exit 1
}

host_os=$(go env GOHOSTOS)
host_arch=$(go env GOHOSTARCH)
files=" $(go list -tags "$tags" -f '{{join .GoFiles " "}}' golang.org/x/net/http2) "
host_files=" $(GOOS=$host_os GOARCH=$host_arch go list -tags "$tags" -f '{{join .GoFiles " "}}' golang.org/x/net/http2) "
module=$(go list -tags "$tags" -f '{{with .Module}}{{.Path}} {{.Version}}{{with .Replace}} => {{.Path}}{{end}}{{end}}' golang.org/x/net/http2)

for file in server.go transport.go frame.go; do
  case $files in
    *" $file "*) ;;
    *) refuse "$module under tags $tags does not compile http2/$file, the implementation the tests below run" ;;
  esac
done
for file in server_wrap.go transport_wrap.go; do
  case $files in
    *" $file "*) refuse "$module under tags $tags compiles http2/$file, the net/http wrapper, instead of x/net's own server and transport" ;;
  esac
done
[ "$files" = "$host_files" ] || refuse "$module compiles other http2 files for the target than for $host_os/$host_arch, so the tests below would not run the target's code"

if ! out=$(GOOS=$host_os GOARCH=$host_arch go test -count=1 -tags "$tags" -run '^(TestServerMaxHandlerGoroutines|TestServerContinuationFlood|TestServerContinuationAfterInvalidHeader)$' -v golang.org/x/net/http2 2>&1); then
  printf '%s\n' "$out" >&2
  refuse "$module failed its early-reset and CONTINUATION tests under tags $tags"
fi
for name in TestServerMaxHandlerGoroutines TestServerContinuationFlood TestServerContinuationAfterInvalidHeader; do
  printf '%s\n' "$out" | grep -q "^--- PASS: $name " || refuse "$module did not run and pass $name under tags $tags"
done

printf 'singbox-http2-guard: %s under tags %s compiles server.go, transport.go and frame.go and passes TestServerMaxHandlerGoroutines, TestServerContinuationFlood and TestServerContinuationAfterInvalidHeader\n' "$module" "$tags"
