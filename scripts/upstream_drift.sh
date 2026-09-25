#!/usr/bin/env bash
set -eu
set -o pipefail

upstream_url=${UPSTREAM_URL:-https://github.com/PasarGuard/node.git}
upstream_slug=${UPSTREAM_SLUG:-PasarGuard/node}
target=${DRIFT_TARGET:-HEAD}
fetch=${UPSTREAM_FETCH:-1}
components=${DRIFT_COMPONENTS:-node xray sing-box sing-quic mtg}
all_components='node xray sing-box sing-quic mtg'
api_url=${GITHUB_API_URL:-https://api.github.com}
categories='rejected|superseded|pending|not-applicable|allowed-merge'
ghsa_re='GHSA(-[23456789cfghjmpqrvwx]{4}){3}'
node_ns=refs/upstream-drift/node

die() {
  printf 'upstream-drift: %s\n' "$*" >&2
  exit 2
}

repo_root=$(git rev-parse --show-toplevel) || die "not inside a git work tree"
cd "$repo_root"
waiver_file=${UPSTREAM_WAIVERS:-.github/upstream-waivers.txt}

for name in $components; do
  case " $all_components " in
    *" $name "*) ;;
    *) die "unknown component '$name' in DRIFT_COMPONENTS (known: $all_components)" ;;
  esac
done
enabled() {
  case " $components " in
    *" $1 "*) return 0 ;;
  esac
  return 1
}

if [ "$(git rev-parse --is-shallow-repository)" != false ]; then
  [ "${DRIFT_ALLOW_SHALLOW:-0}" = 1 ] ||
    die "shallow clone: the full history is required (actions/checkout needs fetch-depth: 0; DRIFT_ALLOW_SHALLOW=1 for an incomplete local run)"
  printf 'upstream-drift: WARNING shallow clone, commits behind the shallow boundary are not seen; the result is not authoritative\n' >&2
fi
target_sha=$(git rev-parse --verify --quiet "$target^{commit}") || die "unknown target $target"
[ -f "$waiver_file" ] || die "waiver file $waiver_file is missing"
command -v curl >/dev/null || die "curl is required"
command -v jq >/dev/null || die "jq is required"

work=$(mktemp -d "${TMPDIR:-/tmp}/upstream-drift.XXXXXX")
trap 'rm -rf "$work"' EXIT
store=${DRIFT_CACHE:-$work/store}
mkdir -p "$store"

today=${DRIFT_TODAY:-$(date -u +%Y-%m-%d)}
printf '%s\n' "$today" | grep -Eqx '[0-9]{4}-[0-9]{2}-[0-9]{2}' || die "DRIFT_TODAY must be YYYY-MM-DD, got: $today"

: >"$work/waivers"
: >"$work/pending"
: >"$work/expired"
line_no=0
while IFS= read -r line || [ -n "$line" ]; do
  line_no=$((line_no + 1))
  printf '%s\n' "$line" | grep -Eq "^([0-9a-f]{40}|$ghsa_re) ($categories): [^[:space:]].{8,}$" ||
    die "$waiver_file:$line_no: expected '<40-hex sha or GHSA id> <rejected|superseded|pending|not-applicable|allowed-merge>: <reason>', got: $line"
  case "$line" in
    *" pending: "*)
      until_date=$(printf '%s\n' "$line" | sed -n 's/.*[[:space:]]until:\([0-9]\{4\}-[0-9]\{2\}-[0-9]\{2\}\).*/\1/p')
      if [ -z "$until_date" ] && ! printf '%s\n' "$line" | grep -Eq '[[:space:]]issue:[^[:space:]]+'; then
        die "$waiver_file:$line_no: a pending waiver needs 'until:YYYY-MM-DD' (at most 30 days ahead) or 'issue:<owner>/<repo>#<number>', got: $line"
      fi
      if [ -n "$until_date" ] && [ "$until_date" \< "$today" ]; then
        printf '%s %s %s\n' "${line%% *}" "$until_date" "${line#* }" >>"$work/expired"
        continue
      fi
      ;;
  esac
  printf '%s\n' "$line" >>"$work/waivers"
done <"$waiver_file"
duplicates=$({ cut -d' ' -f1 "$work/waivers"; cut -d' ' -f1 "$work/expired"; } | sort | uniq -d | tr '\n' ' ')
[ -z "$duplicates" ] || die "$waiver_file lists these ids more than once: $duplicates"
: >"$work/used"

waiver_for() {
  awk -v id="$1" '$1 == id { $1 = ""; sub(/^ /, ""); print; exit }' "$work/waivers"
}

{
  git log --format=%B "$target_sha"
  { git ls-tree -r --name-only "$target_sha" -- patches/ | grep -E '\.patch$' || true; } |
    while IFS= read -r patch_path; do
      git show "$target_sha:$patch_path" | awk '/^diff --git / { exit } { print }'
    done
} | sed -n -e 's/^[[:space:]]*[Uu]pstream:[[:space:]]*\(.*\)$/\1/p' \
  -e 's/.*(cherry picked from commit \([0-9a-fA-F]*\)).*/\1/p' |
  tr -s '[:space:],;()[]/' '\n' | tr '[:upper:]' '[:lower:]' | sort -u >"$work/trailer-tokens"
{ grep -Ex '[0-9a-f]{7,40}' "$work/trailer-tokens" || true; } >"$work/trailers"
{ grep -Ex 'ghsa(-[0-9a-z]{4}){3}' "$work/trailer-tokens" || true; } | sed 's/^ghsa/GHSA/' >"$work/trailer-ghsa"

trailer_ref() {
  awk -v sha="$1" 'index(sha, $0) == 1 { print; exit }' "$work/trailers"
}

patch_ids() {
  git -c diff.noprefix=false -c diff.mnemonicPrefix=false -c core.quotePath=false \
    log --no-merges --no-color --no-ext-diff --no-textconv --no-renames --full-index \
    --src-prefix=a/ --dst-prefix=b/ -p --format='commit %H' "$@" |
    git patch-id --stable
}

api_curl() {
  {
    printf 'header = "Accept: application/vnd.github+json"\n'
    printf 'header = "X-GitHub-Api-Version: 2022-11-28"\n'
    [ -z "${GITHUB_TOKEN:-}" ] || printf 'header = "Authorization: Bearer %s"\n' "$GITHUB_TOKEN"
  } | curl --silent --show-error --location --retry 3 --config - --output "$2" --write-out '%{http_code}' "$1"
}

api_fetch() {
  printf '[]\n' >"$1"
  page=1
  while :; do
    status=$(api_curl "$api_url$2&per_page=100&page=$page" "$work/page.json") ||
      die "GitHub API request $2 failed"
    [ "$status" = 200 ] ||
      die "GitHub API request $2 returned HTTP $status (rate limited? set GITHUB_TOKEN)"
    jq -s '.[0] + .[1]' "$1" "$work/page.json" >"$work/merged.json" ||
      die "GitHub API request $2 did not return a list"
    mv "$work/merged.json" "$1"
    [ "$(jq length "$work/page.json")" -ge 100 ] || break
    page=$((page + 1))
  done
}

total_unported=0
count_unported() {
  total_unported=$((total_unported + 1))
}

note_pending() {
  case "$3" in
    pending:*) printf '%s\t%s\t%s\n' "$2" "$1" "$(printf '%s' "${3#pending:}" | sed 's/^[[:space:]]*//')" >>"$work/pending" ;;
  esac
}

check_advisories() {
  adv_name=$1
  adv_slug=$2
  adv_module=$3
  adv_gitdir=$4
  adv_refs=$5
  adv_excl=$6
  repo_json=$store/$adv_name-repo-advisories.json
  global_json=$store/$adv_name-global-advisories.json
  if [ "$fetch" = 1 ]; then
    api_fetch "$repo_json" "/repos/$adv_slug/security-advisories?state=published"
    api_fetch "$global_json" "/advisories?ecosystem=go&affects=$adv_module"
  fi
  [ -f "$repo_json" ] && [ -f "$global_json" ] ||
    die "$adv_name advisories are not cached in $store; run with UPSTREAM_FETCH=1"
  jq -r '.[] | select(.withdrawn_at == null) |
      [.ghsa_id, (.severity // "unknown"), ((.summary // "") | gsub("[\t\r\n]+"; " "))] | @tsv' \
    "$repo_json" "$global_json" | awk -F'\t' '!seen[$1]++' >"$work/advisories"
  adv_count=0
  while IFS="$(printf '\t')" read -r ghsa severity summary <&4; do
    adv_count=$((adv_count + 1))
    waiver=$(waiver_for "$ghsa")
    [ -z "$waiver" ] || printf '%s\n' "$ghsa" >>"$work/used"
    fixed_by=$(git --git-dir="$adv_gitdir" log -1 --format=%H --regexp-ignore-case --fixed-strings \
      --grep="$ghsa" $adv_excl --)
    ported_by=""
    if ! grep -qx "$ghsa" "$work/trailer-ghsa"; then
      for fix in $(git --git-dir="$adv_gitdir" log --format=%H --regexp-ignore-case --fixed-strings \
        --grep="$ghsa" $(cat "$adv_refs") --not $adv_excl --); do
        if [ -n "$(trailer_ref "$fix")" ]; then
          ported_by=$fix
          break
        fi
      done
    fi
    if [ -n "$fixed_by" ]; then
      status=FIXED
      detail="the pinned history already has $(printf '%.12s' "$fixed_by"), which names this advisory"
    elif grep -qx "$ghsa" "$work/trailer-ghsa"; then
      status=PORTED
      detail="named by an Upstream trailer"
    elif [ -n "$ported_by" ]; then
      status=PORTED
      detail="fixed upstream by $(printf '%.12s' "$ported_by"), which an Upstream trailer ports"
    elif [ -n "$waiver" ]; then
      status=WAIVED
      detail=$waiver
      note_pending "$ghsa" "$adv_name advisory" "$waiver"
    else
      status=UNPORTED
      detail="published advisory, not fixed in the pin, not ported and not waived"
      count_unported
    fi
    printf '%-8s %s advisory %s (%s) %s\n' "$status" "$adv_name" "$ghsa" "$severity" "$summary"
    printf '         %s\n' "$detail"
    if [ "$status" != WAIVED ] && [ -n "$waiver" ]; then
      printf '         note: the waiver for this advisory is no longer needed\n'
    fi
  done 4<"$work/advisories"
  printf 'upstream-drift: %s: %d published advisories checked\n' "$adv_name" "$adv_count"
}

check_node() {
  if [ "$fetch" = 1 ]; then
    git fetch --quiet --prune --no-tags "$upstream_url" \
      "+refs/heads/*:$node_ns/heads/*" "+refs/tags/*:$node_ns/tags/*" ||
      die "fetching $upstream_url failed"
  fi
  git for-each-ref --format='%(refname)' "$node_ns/heads/" "$node_ns/tags/" >"$work/node-refs"
  [ -s "$work/node-refs" ] || die "no upstream refs under $node_ns; run with UPSTREAM_FETCH=1"
  node_refs=$(cat "$work/node-refs")

  printf 'upstream-drift: node: target %s (%s), upstream %s\n' "$target" "$(git rev-parse --short "$target_sha")" "$upstream_url"
  git for-each-ref --format='%(objectname) %(refname)' "$node_ns/heads/" |
    sed "s|$node_ns/heads/|branch |" | sed 's/^/upstream-drift: node: checked /'
  printf 'upstream-drift: node: checked %d upstream tags, newest %s\n' \
    "$(git for-each-ref "$node_ns/tags/" | wc -l | tr -d ' ')" \
    "$(git for-each-ref --sort=-v:refname --count=1 --format='%(refname:strip=4)' "$node_ns/tags/")"

  git rev-list $node_refs | sort -u >"$work/upstream-all"
  git rev-list --parents "$target_sha" --not $node_refs |
    awk 'NR == FNR { up[$1] = 1; next } { for (i = 2; i <= NF; i++) if ($i in up) print $1, $i }' \
      "$work/upstream-all" - >"$work/entry-edges"
  entries=0
  entries_allowed=0
  while read -r sha parent <&8; do
    entries=$((entries + 1))
    waiver=$(waiver_for "$sha")
    case "$waiver" in
      allowed-merge:*)
        printf '%s\n' "$sha" >>"$work/used"
        status=ALLOWED
        detail=$waiver
        entries_allowed=$((entries_allowed + 1))
        ;;
      *)
        status=UNPORTED
        detail="a fork commit with an upstream parent brings upstream history in without porting it: a 'git merge -s ours' of an upstream head looks exactly like this and hides every upstream commit it reaches. Check that it really carries the upstream changes, then add '$sha allowed-merge: <reason>' to $waiver_file"
        count_unported
        ;;
    esac
    printf '%-8s node entry %s (upstream parent %s) %s\n' "$status" "$(git rev-parse --short=12 "$sha")" \
      "$(git rev-parse --short=12 "$parent")" "$(git log -1 --format=%s "$sha")"
    printf '         %s\n' "$detail"
  done 8<"$work/entry-edges"
  printf 'upstream-drift: node: %d fork commits with an upstream parent: %d allowed, %d not allowed\n' \
    "$entries" "$entries_allowed" "$((entries - entries_allowed))"

  : >"$work/upstream-raw"
  while IFS= read -r ref; do
    case $ref in
      "$node_ns/heads/"*) kind=b label=${ref#"$node_ns/heads/"} ;;
      *) kind=t label=tag:${ref#"$node_ns/tags/"} ;;
    esac
    git rev-list --no-merges --reverse "$ref" --not "$target_sha" |
      awk -v suffix=" $kind $label" '{ print $0 suffix }' >>"$work/upstream-raw"
  done <"$work/node-refs"
  awk '{ if (!($1 in seen)) { order[++n] = $1; seen[$1] = 1 }
      if ($2 == "b") b[$1] = ($1 in b) ? b[$1] "," $3 : $3; else t[$1] = ($1 in t) ? t[$1] "," $3 : $3 }
    END { for (i = 1; i <= n; i++) { s = order[i]; print s, ((s in b) ? b[s] : t[s]) } }' \
    "$work/upstream-raw" >"$work/upstream"

  : >"$work/upstream-ids"
  patch_ids "$target_sha" --not $node_refs >"$work/fork-ids"
  if [ -s "$work/upstream" ]; then
    cut -d' ' -f1 "$work/upstream" | patch_ids --no-walk=unsorted --stdin >"$work/upstream-ids"
  fi
  awk 'NR == FNR { fork[$1] = $2; next } ($1 in fork) { print $2, fork[$1] }' \
    "$work/fork-ids" "$work/upstream-ids" >"$work/patch-matches"

  total=0
  ported=0
  empty=0
  waived=0
  unported=0
  while read -r sha labels <&3; do
    total=$((total + 1))
    waiver=$(waiver_for "$sha")
    [ -z "$waiver" ] || printf '%s\n' "$sha" >>"$work/used"
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
      note_pending "$sha" "node commit" "$waiver"
    else
      status=UNPORTED
      detail="not ported and not waived"
      unported=$((unported + 1))
      count_unported
    fi
    printf '%-8s node commit %s [%s] %s\n' "$status" "$(git rev-parse --short=12 "$sha")" "$labels" "$(git log -1 --format=%s "$sha")"
    printf '         %s\n' "$detail"
    if [ "$status" != WAIVED ] && [ -n "$waiver" ]; then
      printf '         note: the waiver for this commit is no longer needed\n'
    fi
  done 3<"$work/upstream"

  printf 'upstream-drift: node: %d upstream commits outside the target: %d ported, %d empty, %d waived, %d unported\n' \
    "$total" "$ported" "$empty" "$waived" "$unported"

  : >"$work/upstream-merges"
  while IFS= read -r ref; do
    git rev-list --merges "$ref" --not "$target_sha" >>"$work/upstream-merges"
  done <"$work/node-refs"
  merges=0
  merges_own=0
  for sha in $(sort -u "$work/upstream-merges"); do
    merges=$((merges + 1))
    git show --cc --format= --no-color "$sha" | grep -q . || continue
    merges_own=$((merges_own + 1))
    waiver=$(waiver_for "$sha")
    [ -z "$waiver" ] || printf '%s\n' "$sha" >>"$work/used"
    trailer=$(trailer_ref "$sha")
    if [ -n "$trailer" ]; then
      status=PORTED
      detail="referenced by an Upstream/cherry-pick trailer ($trailer)"
    elif [ -n "$waiver" ]; then
      status=WAIVED
      detail=$waiver
      note_pending "$sha" "node merge" "$waiver"
    else
      status=UNPORTED
      detail="upstream merge whose combined diff is not empty: it carries changes that none of its parents has (git show --cc $(git rev-parse --short=12 "$sha")), so porting its parents is not enough"
      count_unported
    fi
    printf '%-8s node merge %s %s\n' "$status" "$(git rev-parse --short=12 "$sha")" "$(git log -1 --format=%s "$sha")"
    printf '         %s\n' "$detail"
  done
  printf 'upstream-drift: node: %d upstream merges outside the target, %d with changes of their own\n' "$merges" "$merges_own"
  check_advisories node "$upstream_slug" "$(git show "$target_sha:go.mod" | awk '$1 == "module" { print $2; exit }')" \
    "$(git rev-parse --git-common-dir)" "$work/node-refs" "$target_sha"
}

dockerfile_arg() {
  git show "$target_sha:Dockerfile" | awk -F= -v key="$1" '$1 == "ARG " key { sub(/[[:space:]]+$/, "", $2); print $2; exit }'
}

gomod_version() {
  awk -v module="$1" '{ sub(/^require[[:space:]]+/, "") } $1 == module && $2 != "=>" { print $2; exit }'
}

gomod_replace() {
  awk -v module="$1" '{ for (i = 1; i < NF; i++) if ($i == module && $(i + 1) == "=>") { print $(i + 2), $(i + 3); exit } }'
}

module_rev() {
  if printf '%s\n' "$1" | grep -Eq '[.-][0-9]{14}-[0-9a-f]{12}$'; then
    printf '%s\n' "${1##*-}"
  else
    printf 'refs/tags/%s\n' "${1%+incompatible}"
  fi
}

module_git_url() {
  printf '%s\n' "$1" | awk -F/ '$1 == "github.com" && NF >= 3 { print "https://github.com/" $2 "/" $3 ".git"; ok = 1 } END { exit !ok }' ||
    die "cannot derive a GitHub repository from module $1"
}

tp_open() {
  tp_gd=$store/$1.git
  if [ "$fetch" = 1 ]; then
    [ -d "$tp_gd" ] || git init --quiet --bare "$tp_gd"
    git --git-dir="$tp_gd" config remote.up.url "$2"
    git --git-dir="$tp_gd" config remote.up.promisor true
    git --git-dir="$tp_gd" config remote.up.partialclonefilter tree:0
    git --git-dir="$tp_gd" fetch --quiet --prune --no-tags --filter=tree:0 up \
      '+refs/heads/*:refs/remotes/up/*' '+refs/tags/*:refs/tags/*' || die "fetching $2 failed"
    if [ -n "$3" ]; then
      git --git-dir="$tp_gd" config remote.fork.url "$3"
      git --git-dir="$tp_gd" config remote.fork.promisor true
      git --git-dir="$tp_gd" config remote.fork.partialclonefilter tree:0
      git --git-dir="$tp_gd" fetch --quiet --prune --no-tags --filter=tree:0 fork \
        '+refs/heads/*:refs/remotes/fork/*' || die "fetching $3 failed"
    fi
  fi
  [ -d "$tp_gd" ] || die "$tp_gd is missing; run with UPSTREAM_FETCH=1"
}

tpgit() {
  git --git-dir="$tp_gd" "$@"
}

security_log() {
  tpgit log --no-merges --format=%H --regexp-ignore-case --extended-regexp \
    --grep='GHSA-[[:alnum:]]{4}-' --grep='CVE-[0-9]{4}-' --grep='security' --grep='vulnerab' \
    --grep='exploit' --grep='panic' --grep='crash' --grep='denial[- ]of[- ]service' \
    --grep='(^|[^[:alnum:]])d?dos([^[:alnum:]]|$)' --grep='(^|[^[:alnum:]])races?([^[:alnum:]]|$)' \
    --grep='overflow' --grep='out[- ]of[- ]bounds' --grep='unbounded|not bounded' --grep='untrusted' \
    --grep='leak' --grep='deadlock' --grep='infinite loop' --grep='bypass' --grep='exhaust' "$@"
}

check_thirdparty() {
  name=$1
  slug=$2
  module=$3
  line=$4
  pin_rev=$5
  pin_desc=$6
  fork_rev=$7
  tp_open "$name" "https://github.com/$slug.git" "$8"

  pin_sha=$(tpgit rev-parse --verify --quiet "$pin_rev^{commit}") ||
    die "$name: the pin $pin_rev ($pin_desc) is not a commit of $slug"
  excl=$pin_sha
  fork_note=""
  if [ -n "$fork_rev" ]; then
    fork_sha=$(tpgit rev-parse --verify --quiet "$fork_rev^{commit}") ||
      die "$name: the fork commit $fork_rev is not on any branch of $8"
    tpgit merge-base --is-ancestor "$pin_sha" "$fork_sha" ||
      die "$name: the fork commit $fork_rev does not contain the upstream commit $pin_rev that go.mod requires"
    excl="$pin_sha $fork_sha"
    fork_note=", fork $(printf '%.12s' "$fork_sha") contains it"
  fi
  line_sha=$(tpgit rev-parse --verify --quiet "refs/remotes/up/$line^{commit}") ||
    die "$name: upstream branch $line of $slug is gone"
  pin_date=$(tpgit log -1 --format=%ct "$pin_sha")

  tpgit for-each-ref --format='%(committerdate:unix) %(refname)' refs/remotes/up/ |
    awk -v d="$pin_date" -v line="refs/remotes/up/$line" '$2 == line { print $2; next } $1 > d { rest[++n] = $2 }
      END { for (i = 1; i <= n; i++) print rest[i] }' >"$work/$name-refs"
  refs=$(cat "$work/$name-refs")
  printf 'upstream-drift: %s: %s, pin %s (%s)%s\n' "$name" "$slug" "$(printf '%.12s' "$pin_sha")" "$pin_desc" "$fork_note"
  printf 'upstream-drift: %s: checked %d branches active since the pin; %s at %s\n' \
    "$name" "$(wc -l <"$work/$name-refs" | tr -d ' ')" "$line" "$line_sha"

  : >"$work/$name-raw"
  while IFS= read -r ref; do
    tpgit rev-list --no-merges "$ref" --not $excl |
      awk -v label="${ref#refs/remotes/up/}" '{ print $1, label }' >>"$work/$name-raw"
  done <"$work/$name-refs"
  awk '{ n[$1]++; if (!($1 in first)) first[$1] = $2 }
    END { for (s in n) print s, (n[s] > 1 ? first[s] " +" (n[s] - 1) : first[s]) }' \
    "$work/$name-raw" >"$work/$name-labels"

  commits=0
  unported=0
  security_log $refs --not $excl -- >"$work/$name-security"
  while read -r sha <&5; do
    commits=$((commits + 1))
    waiver=$(waiver_for "$sha")
    [ -z "$waiver" ] || printf '%s\n' "$sha" >>"$work/used"
    trailer=$(trailer_ref "$sha")
    if [ -n "$trailer" ]; then
      status=PORTED
      detail="referenced by an Upstream/cherry-pick trailer ($trailer)"
    elif [ -n "$waiver" ]; then
      status=WAIVED
      detail=$waiver
      note_pending "$sha" "$name commit" "$waiver"
    else
      status=UNPORTED
      detail="security-looking upstream commit after the pin, not ported and not waived"
      unported=$((unported + 1))
      count_unported
    fi
    printf '%-8s %s commit %s [%s] %s\n' "$status" "$name" "$(printf '%.12s' "$sha")" \
      "$(awk -v s="$sha" '$1 == s { $1 = ""; sub(/^ /, ""); print; exit }' "$work/$name-labels")" \
      "$(tpgit log -1 --format=%s "$sha")"
    printf '         %s\n' "$detail"
    if [ "$status" != WAIVED ] && [ -n "$waiver" ]; then
      printf '         note: the waiver for this commit is no longer needed\n'
    fi
  done 5<"$work/$name-security"

  releases=0
  tpgit for-each-ref --sort=v:refname --format='%(refname:strip=2) %(creatordate:unix)' refs/tags/ |
    awk -v d="$pin_date" '$1 ~ /^v?[0-9]+(\.[0-9]+)+$/ && $2 > d { print $1 }' >"$work/$name-tags"
  while IFS= read -r tag <&6; do
    tag_sha=$(tpgit rev-parse "refs/tags/$tag^{commit}")
    contained=0
    for e in $excl; do
      if tpgit merge-base --is-ancestor "$tag_sha" "$e"; then
        contained=1
      fi
    done
    [ "$contained" = 0 ] || continue
    releases=$((releases + 1))
    waiver=$(waiver_for "$tag_sha")
    if [ -n "$waiver" ]; then
      printf '%s\n' "$tag_sha" >>"$work/used"
      status=WAIVED
      detail=$waiver
      note_pending "$tag_sha" "$name release $tag" "$waiver"
    else
      status=UNPORTED
      detail="upstream release after the pin, neither taken nor waived (waive it by its commit $tag_sha)"
      unported=$((unported + 1))
      count_unported
    fi
    printf '%-8s %s release %s (%s)\n' "$status" "$name" "$tag" "$(printf '%.12s' "$tag_sha")"
    printf '         %s\n' "$detail"
  done 6<"$work/$name-tags"

  printf 'upstream-drift: %s: %d security-looking commits and %d releases after the pin, %d unported\n' \
    "$name" "$commits" "$releases" "$unported"
  check_advisories "$name" "$slug" "$module" "$tp_gd" "$work/$name-refs" "$excl"
}

if enabled node; then
  check_node
fi

if enabled xray; then
  xray_commit=$(dockerfile_arg XRAY_COMMIT)
  printf '%s\n' "$xray_commit" | grep -Eqx '[0-9a-f]{40}' ||
    die "Dockerfile ARG XRAY_COMMIT is not a full commit sha: '$xray_commit'"
  check_thirdparty xray XTLS/Xray-core github.com/xtls/xray-core main \
    "$xray_commit" "Dockerfile ARG XRAY_COMMIT" "" ""
fi

if enabled sing-box; then
  singbox_version=$(dockerfile_arg SINGBOX_VERSION)
  [ -n "$singbox_version" ] || die "Dockerfile has no ARG SINGBOX_VERSION"
  check_thirdparty sing-box SagerNet/sing-box github.com/sagernet/sing-box oldstable \
    "refs/tags/$singbox_version" "Dockerfile ARG SINGBOX_VERSION $singbox_version" "" ""
fi

if enabled sing-quic; then
  singbox_repo=$(dockerfile_arg SINGBOX_REPO)
  singbox_commit=$(dockerfile_arg SINGBOX_COMMIT)
  printf '%s\n' "$singbox_commit" | grep -Eqx '[0-9a-f]{40}' ||
    die "Dockerfile ARG SINGBOX_COMMIT is not a full commit sha: '$singbox_commit'"
  fork_gd=$store/sing-box-fork.git
  if [ "$fetch" = 1 ]; then
    [ -d "$fork_gd" ] || git init --quiet --bare "$fork_gd"
    git --git-dir="$fork_gd" config remote.fork.url "$singbox_repo"
    git --git-dir="$fork_gd" config remote.fork.promisor true
    git --git-dir="$fork_gd" config remote.fork.partialclonefilter blob:none
    git --git-dir="$fork_gd" fetch --quiet --depth=1 --no-tags --filter=blob:none fork "$singbox_commit" ||
      die "fetching $singbox_commit from $singbox_repo failed"
  fi
  singquic_version=$(git --git-dir="$fork_gd" show "$singbox_commit:go.mod" | gomod_version github.com/sagernet/sing-quic) ||
    die "cannot read go.mod of $singbox_repo at $singbox_commit"
  [ -n "$singquic_version" ] || die "the sing-box fork go.mod does not require github.com/sagernet/sing-quic"
  check_thirdparty sing-quic SagerNet/sing-quic github.com/sagernet/sing-quic main \
    "$(module_rev "$singquic_version")" "sing-box fork go.mod requires $singquic_version" "" ""
fi

if enabled mtg; then
  node_gomod=$(git show "$target_sha:go.mod")
  mtg_version=$(printf '%s\n' "$node_gomod" | gomod_version github.com/9seconds/mtg/v2)
  [ -n "$mtg_version" ] || die "go.mod does not require github.com/9seconds/mtg/v2"
  mtg_replace=$(printf '%s\n' "$node_gomod" | gomod_replace github.com/9seconds/mtg/v2)
  mtg_fork_rev=""
  mtg_fork_url=""
  if [ -n "$mtg_replace" ]; then
    mtg_fork_url=$(module_git_url "${mtg_replace%% *}")
    mtg_fork_rev=$(module_rev "${mtg_replace#* }")
  fi
  check_thirdparty mtg 9seconds/mtg github.com/9seconds/mtg/v2 master \
    "$(module_rev "$mtg_version")" "go.mod requires $mtg_version" "$mtg_fork_rev" "$mtg_fork_url"
fi

if [ "$components" = "$all_components" ]; then
  while read -r id _ <&7; do
    if ! grep -qx "$id" "$work/used"; then
      printf 'note: waiver %s matches nothing this run checked; remove it\n' "$id"
    fi
  done 7<"$work/waivers"
fi

expired_count=$(wc -l <"$work/expired" | tr -d ' ')
if [ "$expired_count" -gt 0 ]; then
  printf '\n==================== EXPIRED WAIVERS (%d) ====================\n' "$expired_count"
  printf 'these pending waivers passed their until: date on or before %s and no longer waive anything\n' "$today"
  sort "$work/expired" | awk '{ id = $1; d = $2; $1 = ""; $2 = ""; sub(/^  /, ""); printf "EXPIRED  %s until %s\n         %s\n", id, d, $0 }'
  printf '===============================================================\n'
fi
pending_count=$(wc -l <"$work/pending" | tr -d ' ')
printf '\n==================== PENDING WAIVERS (%d) ====================\n' "$pending_count"
printf 'waived as pending: they do not fail this job, but each one still needs a port or a decision\n'
sort -t "$(printf '\t')" -k1,1 -k2,2 "$work/pending" |
  awk -F '\t' '{ printf "PENDING  %s %s\n         %s\n", $1, $2, $3 }'
printf '===============================================================\n\n'
printf 'upstream-drift: %d unported items across [%s]\n' "$total_unported" "$components"
if [ "$total_unported" -gt 0 ]; then
  printf 'upstream-drift: port each UNPORTED item with an "Upstream: <full sha or GHSA id>" line in the porting commit\n'
  printf 'upstream-drift: or in the header of a patches/*.patch file, or add "<full sha or GHSA id> <category>: <reason>"\n'
  printf 'upstream-drift: to %s; see docs/upstream-parity.md\n' "$waiver_file"
  exit 1
fi
