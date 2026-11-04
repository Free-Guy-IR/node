#!/usr/bin/env bash
set -eu
set -o pipefail

root=${1:-$(git rev-parse --show-toplevel)}
dockerfile=$root/Dockerfile
doc=$root/docs/upstream-parity.md
remote=${XRAY_PIN_LABEL_REMOTE:-https://github.com/XTLS/Xray-core.git}

fail() {
  printf 'xray-pin-label: %s\n' "$1" >&2
  exit 1
}

[ "$(grep -c '^ARG XRAY_VERSION=' "$dockerfile")" = 1 ] || fail "Dockerfile needs exactly one ARG XRAY_VERSION= line"
[ "$(grep -c '^ARG XRAY_COMMIT=' "$dockerfile")" = 1 ] || fail "Dockerfile needs exactly one ARG XRAY_COMMIT= line"
tag=$(sed -n 's/^ARG XRAY_VERSION=//p' "$dockerfile")
commit=$(sed -n 's/^ARG XRAY_COMMIT=//p' "$dockerfile")
printf '%s' "$commit" | grep -Eqx '[0-9a-f]{40}' || fail "Dockerfile ARG XRAY_COMMIT is not a full sha: '$commit'"
printf '%s' "$tag" | grep -Eqx 'v[0-9]+\.[0-9]+\.[0-9]+' || fail "Dockerfile ARG XRAY_VERSION is not a release tag: '$tag'"

lines=$(grep -E '^- Xray pin: ' "$doc" || true)
[ "$(printf '%s\n' "$lines" | grep -c .)" = 1 ] || fail "$doc needs exactly one '- Xray pin: ' line"

tag_commit=$(printf '%s\n' "$lines" | sed -nE "s/^- Xray pin: \`$tag\` \(\`([0-9a-f]{40})\`\).*/\1/p")
[ -n "$tag_commit" ] || fail "the '- Xray pin: ' line must start with \`$tag\` (\`<full sha of the tag commit>\`)"

git_dir=$(mktemp -d "${TMPDIR:-/tmp}/xray-pin-label.XXXXXX")
trap 'rm -rf "$git_dir"' EXIT
git init -q --bare "$git_dir"
git -C "$git_dir" fetch -q --filter=tree:0 "$remote" "$commit" "refs/tags/$tag:refs/tags/$tag" ||
  fail "cannot fetch $tag and $commit from $remote"
remote_tag_commit=$(git -C "$git_dir" rev-parse "refs/tags/$tag^{commit}")
[ "$remote_tag_commit" = "$tag_commit" ] || fail "$remote tag $tag is $remote_tag_commit, not $tag_commit"
git -C "$git_dir" merge-base --is-ancestor "$tag_commit" "$commit" ||
  fail "ARG XRAY_COMMIT $commit does not descend from the $tag tag commit $tag_commit"
ahead=$(git -C "$git_dir" rev-list --count "$tag_commit..$commit")

if [ "$ahead" = 0 ]; then
  want="- Xray pin: \`$tag\` (\`$tag_commit\`), the release tag commit itself"
  [ "$lines" = "$want" ] || fail "ARG XRAY_COMMIT is the tag commit, so the line must read: $want"
else
  want="- Xray pin: \`$tag\` (\`$tag_commit\`) plus $ahead \`main\` commits (\`$commit\`)"
  [ "$lines" = "$want" ] || fail "ARG XRAY_COMMIT is $ahead commits past the $tag tag commit, so the line must read: $want"
fi

printf 'xray-pin-label: %s (%.12s), build %.12s\n' "$tag" "$tag_commit" "$commit"
