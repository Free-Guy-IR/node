#!/usr/bin/env bash
set -u

root=$(git rev-parse --show-toplevel) || exit 2
cd "$root" || exit 2

work=$(mktemp -d "${TMPDIR:-/tmp}/image-tag-policy-test.XXXXXX")
trap 'rm -rf "$work"' EXIT

mkdir -p "$work/bin"
cat >"$work/bin/curl" <<'SH'
#!/usr/bin/env bash
printf '404'
SH
chmod +x "$work/bin/curl"

failures=0
digest=$(printf 'b%.0s' $(seq 64))

policy() {
  env -i PATH="$work/bin:/usr/bin:/bin" GITHUB_API_URL=https://api.invalid GITHUB_REPOSITORY=Free-Guy-IR/node GH_TOKEN=t \
    bash scripts/image_tag_policy.sh "$@" >"$work/log" 2>&1
}

must_refuse() {
  if policy "$1"; then
    printf 'FAIL %s is accepted: %s\n' "$1" "$2"
    failures=$((failures + 1))
  else
    printf 'ok   %s is refused\n' "$1"
  fi
}

must_accept() {
  if policy "$1"; then
    printf 'ok   %s is accepted\n' "$1"
  else
    printf 'FAIL %s is refused: %s\n' "$1" "$(tail -1 "$work/log")"
    failures=$((failures + 1))
  fi
}

must_accept nightly
must_refuse latest "latest belongs to Publish Release"
must_refuse v0.6.21 "release tags are immutable"
must_refuse "sha256-${digest}.sig" "it is the cosign signature tag of the image with that digest"
must_refuse "sha256-${digest}.att" "it is the cosign attestation tag of the image with that digest"
must_refuse "sha256-${digest}" "it is the OCI referrers fallback tag of the image with that digest"

if [ "$failures" -gt 0 ]; then
  printf 'image tag policy test: %d failures\n' "$failures"
  exit 1
fi
printf 'image tag policy test: every reserved tag is refused\n'
