#!/usr/bin/env bash
set -eu
set -o pipefail

upstream_url=${UPSTREAM_URL:-https://github.com/PasarGuard/node.git}
upstream_branches=${UPSTREAM_BRANCHES:-main dev}
target=${DRIFT_TARGET:-HEAD}
fetch=${UPSTREAM_FETCH:-1}
categories='rejected|superseded|pending|not-applicable'

die() {
  printf 'upstream-drift: %s\n' "$*" >&2
  exit 2
}

repo_root=$(git rev-parse --show-toplevel) || die "not inside a git work tree"
cd "$repo_root"
waiver_file=${UPSTREAM_WAIVERS:-.github/upstream-waivers.txt}

if [ "$(git rev-parse --is-shallow-repository)" != false ]; then
  [ "${DRIFT_ALLOW_SHALLOW:-0}" = 1 ] ||
    die "shallow clone: the full history is required (actions/checkout needs fetch-depth: 0; DRIFT_ALLOW_SHALLOW=1 for an incomplete local run)"
  printf 'upstream-drift: WARNING shallow clone, commits behind the shallow boundary are not seen; the result is not authoritative\n' >&2
fi
target_sha=$(git rev-parse --verify --quiet "$target^{commit}") || die "unknown target $target"
[ -f "$waiver_file" ] || die "waiver file $waiver_file is missing"

work=$(mktemp -d "${TMPDIR:-/tmp}/upstream-drift.XXXXXX")
trap 'rm -rf "$work"' EXIT

upstream_refs=""
set --
for branch in $upstream_branches; do
  upstream_refs="$upstream_refs refs/remotes/upstream/$branch"
  set -- "$@" "+refs/heads/$branch:refs/remotes/upstream/$branch"
done
[ -n "$upstream_refs" ] || die "UPSTREAM_BRANCHES is empty"

if [ "$fetch" = 1 ]; then
  git fetch --quiet --no-tags "$upstream_url" "$@" || die "fetching $upstream_url failed"
fi
for ref in $upstream_refs; do
  git rev-parse --verify --quiet "$ref^{commit}" >/dev/null ||
    die "$ref does not exist; run with UPSTREAM_FETCH=1"
done

: >"$work/waivers"
line_no=0
while IFS= read -r line || [ -n "$line" ]; do
  line_no=$((line_no + 1))
  printf '%s\n' "$line" | grep -Eq "^[0-9a-f]{40} ($categories): [^[:space:]].{8,}$" ||
    die "$waiver_file:$line_no: expected '<40-hex upstream sha> <rejected|superseded|pending|not-applicable>: <reason>', got: $line"
  printf '%s\n' "$line" >>"$work/waivers"
done <"$waiver_file"
duplicates=$(cut -d' ' -f1 "$work/waivers" | sort | uniq -d | tr '\n' ' ')
[ -z "$duplicates" ] || die "$waiver_file lists these upstream commits more than once: $duplicates"

patch_ids() {
  git -c diff.noprefix=false -c diff.mnemonicPrefix=false -c core.quotePath=false \
    log --no-merges --no-color --no-ext-diff --no-textconv --no-renames --full-index \
    --src-prefix=a/ --dst-prefix=b/ -p --format='commit %H' "$@" |
    git patch-id --stable
}

: >"$work/upstream-raw"
for branch in $upstream_branches; do
  git rev-list --no-merges --reverse "refs/remotes/upstream/$branch" --not "$target_sha" |
    sed "s|\$| $branch|" >>"$work/upstream-raw"
done
awk '{ if (!($1 in seen)) { order[++n] = $1; seen[$1] = $2 } else { seen[$1] = seen[$1] "," $2 } }
  END { for (i = 1; i <= n; i++) print order[i], seen[order[i]] }' "$work/upstream-raw" >"$work/upstream"

: >"$work/upstream-ids"
patch_ids "$target_sha" --not $upstream_refs >"$work/fork-ids"
if [ -s "$work/upstream" ]; then
  cut -d' ' -f1 "$work/upstream" | patch_ids --no-walk=unsorted --stdin >"$work/upstream-ids"
fi
awk 'NR == FNR { fork[$1] = $2; next } ($1 in fork) { print $2, fork[$1] }' \
  "$work/fork-ids" "$work/upstream-ids" >"$work/patch-matches"

git log --format=%B "$target_sha" |
  sed -n -e 's/^[[:space:]]*[Uu]pstream:[[:space:]]*\(.*\)$/\1/p' \
    -e 's/.*(cherry picked from commit \([0-9a-fA-F]*\)).*/\1/p' |
  tr -s '[:space:],;()[]' '\n' | tr 'A-F' 'a-f' |
  { grep -Ex '[0-9a-f]{7,40}' || true; } | sort -u >"$work/trailers"

trailer_ref() {
  awk -v sha="$1" 'index(sha, $0) == 1 { print; exit }' "$work/trailers"
}

total=0
ported=0
empty=0
waived=0
unported=0
printf 'upstream-drift: target %s (%s), upstream %s [%s]\n' \
  "$target" "$(git rev-parse --short "$target_sha")" "$upstream_url" "$upstream_branches"
while read -r sha branches <&3; do
  total=$((total + 1))
  short=$(git rev-parse --short=12 "$sha")
  subject=$(git log -1 --format=%s "$sha")
  waiver=$(awk -v sha="$sha" '$1 == sha { $1 = ""; sub(/^ /, ""); print; exit }' "$work/waivers")
  fork_match=$(awk -v sha="$sha" '$1 == sha { print $2; exit }' "$work/patch-matches")
  trailer=$(trailer_ref "$sha")
  parent_tree=$(git rev-parse --verify --quiet "$sha^1^{tree}" || true)
  if [ -n "$fork_match" ]; then
    status=PORTED
    detail="same patch-id as $(git rev-parse --short=12 "$fork_match")"
    ported=$((ported + 1))
  elif [ -n "$trailer" ]; then
    status=PORTED
    detail="referenced by an Upstream/cherry-pick trailer ($trailer)"
    ported=$((ported + 1))
  elif [ "$(git rev-parse "$sha^{tree}")" = "$parent_tree" ]; then
    status=EMPTY
    detail="changes no file, nothing to port"
    empty=$((empty + 1))
  elif [ -n "$waiver" ]; then
    status=WAIVED
    detail=$waiver
    waived=$((waived + 1))
  else
    status=UNPORTED
    detail="not ported and not waived"
    unported=$((unported + 1))
  fi
  printf '%-8s %s [%s] %s\n' "$status" "$short" "$branches" "$subject"
  printf '         %s\n' "$detail"
  if [ "$status" != WAIVED ] && [ -n "$waiver" ]; then
    printf '         note: the waiver for this commit is no longer needed\n'
  fi
done 3<"$work/upstream"

while read -r sha _ <&3; do
  if ! grep -q "^$sha " "$work/upstream"; then
    printf 'note: waiver %s matches no upstream commit outside the target; remove it\n' "$sha"
  fi
done 3<"$work/waivers"

printf 'upstream-drift: %d upstream commits outside the target: %d ported, %d empty, %d waived, %d unported\n' \
  "$total" "$ported" "$empty" "$waived" "$unported"
if [ "$unported" -gt 0 ]; then
  printf 'upstream-drift: port each UNPORTED commit with an "Upstream: <full sha>" trailer (or git cherry-pick -x),\n'
  printf 'upstream-drift: or add "<full sha> <%s>: <reason>" to %s; see docs/upstream-parity.md\n' \
    "$categories" "$waiver_file"
  exit 1
fi
