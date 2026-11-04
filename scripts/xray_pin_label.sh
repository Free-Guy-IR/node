#!/usr/bin/env bash
set -eu
set -o pipefail

root=${1:-$(git rev-parse --show-toplevel)}
dockerfile=$root/Dockerfile
doc=$root/docs/upstream-parity.md
remote=${XRAY_PIN_LABEL_REMOTE-https://github.com/XTLS/Xray-core.git}

fail() {
  printf 'xray-pin-label: %s\n' "$1" >&2
  exit 1
}

tag=$(sed -n 's/^ARG XRAY_VERSION=//p' "$dockerfile")
commit=$(sed -n 's/^ARG XRAY_COMMIT=//p' "$dockerfile")
printf '%s' "$commit" | grep -Eqx '[0-9a-f]{40}' || fail "Dockerfile ARG XRAY_COMMIT is not a full sha: '$commit'"
printf '%s' "$tag" | grep -Eqx 'v[0-9]+\.[0-9]+\.[0-9]+' || fail "Dockerfile ARG XRAY_VERSION is not a release tag: '$tag'"

lines=$(grep -E '^- Xray pin: ' "$doc" || true)
[ "$(printf '%s\n' "$lines" | grep -c .)" = 1 ] || fail "$doc needs exactly one '- Xray pin: ' line"

tag_commit=$(printf '%s\n' "$lines" | sed -nE "s/^- Xray pin: \`$tag\` \(\`([0-9a-f]{40})\`\).*/\1/p")
[ -n "$tag_commit" ] || fail "the '- Xray pin: ' line must start with \`$tag\` (\`<full sha of the tag commit>\`)"

if [ "$tag_commit" = "$commit" ]; then
  want="- Xray pin: \`$tag\` (\`$tag_commit\`), the release tag commit itself"
  [ "$lines" = "$want" ] || fail "ARG XRAY_COMMIT is the tag commit, so the line must read: $want"
else
  ahead_re="^- Xray pin: \`$tag\` \(\`$tag_commit\`\) plus [1-9][0-9]* \`main\` commits \(\`$commit\`\)$"
  printf '%s\n' "$lines" | grep -Eq "$ahead_re" ||
    fail "ARG XRAY_COMMIT $commit is not the $tag tag commit $tag_commit, so the line must name it as '$tag plus N \`main\` commits'"
fi

if [ -n "$remote" ]; then
  refs=$(git ls-remote "$remote" "refs/tags/$tag" "refs/tags/$tag^{}") || fail "cannot list $tag on $remote"
  remote_commit=$(printf '%s\n' "$refs" | awk -v p="refs/tags/$tag^{}" '$2 == p { print $1 }')
  [ -n "$remote_commit" ] || remote_commit=$(printf '%s\n' "$refs" | awk -v p="refs/tags/$tag" '$2 == p { print $1 }')
  [ -n "$remote_commit" ] || fail "$remote has no tag $tag"
  [ "$remote_commit" = "$tag_commit" ] || fail "$remote tag $tag is $remote_commit, not $tag_commit"
fi

printf 'xray-pin-label: %s (%.12s), build %.12s\n' "$tag" "$tag_commit" "$commit"
