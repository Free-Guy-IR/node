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
max_api_pages=${DRIFT_MAX_API_PAGES:-50}
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
git show --remerge-diff --format= "$target_sha" >/dev/null 2>&1 ||
  die "git $(git --version | awk '{ print $3 }') has no --remerge-diff; git 2.36 or newer is required"
[ -f "$waiver_file" ] || die "waiver file $waiver_file is missing"
command -v curl >/dev/null || die "curl is required"
command -v jq >/dev/null || die "jq is required"

work=$(mktemp -d "${TMPDIR:-/tmp}/upstream-drift.XXXXXX")
trap 'rm -rf "$work"' EXIT
store=${DRIFT_CACHE:-$work/store}
mkdir -p "$store"

day_number() {
  printf '%s\n' "$1" | awk -F- 'NF == 3 && $1 ~ /^[0-9][0-9][0-9][0-9]$/ && $2 ~ /^[0-9][0-9]$/ && $3 ~ /^[0-9][0-9]$/ {
      y = $1 + 0; m = $2 + 0; d = $3 + 0
      split("31 28 31 30 31 30 31 31 30 31 30 31", len, " ")
      if ((y % 4 == 0 && y % 100 != 0) || y % 400 == 0) len[2] = 29
      if (m < 1 || m > 12 || d < 1 || d > len[m]) exit 1
      if (m < 3) { y--; m += 12 }
      print 365 * y + int(y / 4) - int(y / 100) + int(y / 400) + int((153 * (m - 3) + 2) / 5) + d
      ok = 1
    }
    END { exit !ok }'
}

today=${DRIFT_TODAY:-$(date -u +%Y-%m-%d)}
today_day=$(day_number "$today") || die "DRIFT_TODAY must be a calendar date YYYY-MM-DD, got: $today"
max_waiver_days=30

: >"$work/waivers"
: >"$work/pending"
: >"$work/expired"
: >"$work/waiver-branches"
line_no=0
while IFS= read -r line || [ -n "$line" ]; do
  line_no=$((line_no + 1))
  printf '%s\n' "$line" | grep -Eq "^([0-9a-f]{40}|$ghsa_re) ($categories): [^[:space:]].{8,}$" ||
    die "$waiver_file:$line_no: expected '<40-hex sha or GHSA id> <rejected|superseded|pending|not-applicable|allowed-merge>: <reason>', got: $line"
  branch_tokens=$(printf '%s\n' "$line" | tr -s '[:space:]' '\n' | { grep -i '^branch:' || true; })
  if [ -n "$branch_tokens" ]; then
    bad_branch=$(printf '%s\n' "$branch_tokens" | { grep -Evx 'branch:[A-Za-z0-9._/-]+' || true; })
    [ -z "$bad_branch" ] ||
      die "$waiver_file:$line_no: a branch scope must be written branch:<ref>, got: $bad_branch"
  fi
  if printf '%s\n' "$line" | grep -Eiq 'not merged|never merged|revisit if upstream merges'; then
    [ -n "$branch_tokens" ] ||
      die "$waiver_file:$line_no: a waiver that says the upstream work is unmerged must scope it with a branch:<ref> token, so the check turns the item UNPORTED the day it is merged into any other ref; got: $line"
  fi
  case "$line" in
    *" pending: "*)
      tokens=$(printf '%s\n' "${line#* pending: }" | tr -s '[:space:]' '\n')
      until_tokens=$(printf '%s\n' "$tokens" | { grep -i 'until:' || true; })
      issue_tokens=$(printf '%s\n' "$tokens" | { grep -i '^issue:' || true; })
      [ -n "$until_tokens" ] ||
        die "$waiver_file:$line_no: a pending waiver needs exactly one 'until:YYYY-MM-DD', at most $max_waiver_days days after $today (an issue: reference is optional and does not replace it), got: $line"
      [ "$(printf '%s\n' "$until_tokens" | wc -l | tr -d ' ')" = 1 ] ||
        die "$waiver_file:$line_no: a pending waiver takes exactly one until: date, got: $line"
      printf '%s\n' "$until_tokens" | grep -Eqx 'until:[0-9]{4}-[0-9]{2}-[0-9]{2}' ||
        die "$waiver_file:$line_no: until: must be written until:YYYY-MM-DD with no space, got: $until_tokens"
      until_date=${until_tokens#until:}
      until_day=$(day_number "$until_date") ||
        die "$waiver_file:$line_no: until:$until_date is not a calendar date"
      [ "$until_day" -le $((today_day + max_waiver_days)) ] ||
        die "$waiver_file:$line_no: until:$until_date is more than $max_waiver_days days after $today"
      if [ -n "$issue_tokens" ]; then
        bad_issue=$(printf '%s\n' "$issue_tokens" | { grep -Evx 'issue:[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+#[0-9]+' || true; })
        [ -z "$bad_issue" ] ||
          die "$waiver_file:$line_no: an issue reference must be written issue:<owner>/<repo>#<number>, got: $bad_issue"
      fi
      if [ "$until_day" -lt "$today_day" ]; then
        printf '%s %s %s\n' "${line%% *}" "$until_date" "${line#* }" >>"$work/expired"
        continue
      fi
      ;;
  esac
  printf '%s\n' "$line" >>"$work/waivers"
  [ -z "$branch_tokens" ] ||
    printf '%s %s\n' "${line%% *}" "$(printf '%s\n' "$branch_tokens" | sed 's/^[Bb][Rr][Aa][Nn][Cc][Hh]://' | tr '\n' ' ')" >>"$work/waiver-branches"
done <"$waiver_file"
duplicates=$({ cut -d' ' -f1 "$work/waivers"; cut -d' ' -f1 "$work/expired"; } | sort | uniq -d | tr '\n' ' ')
[ -z "$duplicates" ] || die "$waiver_file lists these ids more than once: $duplicates"
: >"$work/used"

waiver_for() {
  awk -v id="$1" '$1 == id { $1 = ""; sub(/^ /, ""); print; exit }' "$work/waivers"
}

waiver_branch_scope() {
  awk -v id="$1" '$1 == id { $1 = ""; sub(/^ /, ""); print; exit }' "$work/waiver-branches"
}

branch_only_heads() {
  awk -v lines=" $1 " '$0 !~ /^(renovate|dependabot)\// { h[++n] = $0; if (index(lines, " " $0 " ")) d = 1 }
    END { if (!d) for (i = 1; i <= n; i++) printf "%s%s", (i > 1 ? " " : ""), h[i] }'
}

node_branch_only() {
  [ -z "$(git for-each-ref --count=1 --contains "$1" "$node_ns/tags/")" ] || return 0
  git for-each-ref --format='%(refname:strip=4)' --contains "$1" "$node_ns/heads/" | branch_only_heads "main dev"
}

node_scope_violations() {
  allowed=" $2 "
  { git for-each-ref --format='%(refname:strip=4)' --contains "$1" "$node_ns/heads/" |
      awk -v a="$allowed" '$0 !~ /^(renovate|dependabot)\// && index(a, " " $0 " ") == 0 { print "branch " $0 }'
    git for-each-ref --format='%(refname:strip=4)' --contains "$1" "$node_ns/tags/" |
      awk '{ print "tag " $0 }'
  } | paste -sd ',' - | sed 's/,/, /g'
}

trailer_tokens() {
  sed -n -e 's/^[[:space:]]*[Uu]pstream:[[:space:]]*\(.*\)$/\1/p' \
    -e 's/.*(cherry picked from commit \([0-9a-fA-F]*\)).*/\1/p' |
    tr -s '[:space:],;()[]/' '\n' | tr '[:upper:]' '[:lower:]' | sort -u
}

split_tokens() {
  { grep -Ex '[0-9a-f]{7,40}' "$1" || true; } >"$work/trailers$2"
  { grep -Ex 'ghsa(-[0-9a-z]{4}){3}' "$1" || true; } | sed 's/^ghsa/GHSA/' >"$work/trailer-ghsa$2"
}

git log --format=%B "$target_sha" | trailer_tokens >"$work/commit-tokens"
split_tokens "$work/commit-tokens" ""

dockerfile_text=$(git show "$target_sha:Dockerfile" 2>/dev/null || true)
makefile_text=$(git show "$target_sha:Makefile" 2>/dev/null || true)

makefile_applies_xray_patches() {
  printf '%s\n' "$makefile_text" | awk '
    /^[[:space:]]*#/ { next }
    index($0, "patches/xray-*.patch") && $0 ~ /(^|[[:space:]])apply([[:space:]]|$)/ &&
      $0 !~ /--check|--stat|--numstat|--summary|--reverse|(^|[[:space:]])-R([[:space:]]|$)|[|][|][[:space:]]*(true|:)/ { found = 1 }
    END { exit !found }'
}

dockerfile_program='
  function taint(why) {
    if (!tainted) reason = why
    tainted = 1
  }
  function norm(p,   parts, n, i, out, depth, stack) {
    if (substr(p, 1, 1) != "/") return p
    n = split(p, parts, "/")
    depth = 0
    for (i = 1; i <= n; i++) {
      if (parts[i] == "" || parts[i] == ".") continue
      if (parts[i] == "..") { if (depth > 0) depth--; continue }
      stack[++depth] = parts[i]
    }
    out = ""
    for (i = 1; i <= depth; i++) out = out "/" stack[i]
    return out == "" ? "/" : out
  }
  function strip_comment(c,   out, i, ch, prev, sq, dq) {
    out = ""
    prev = ""
    sq = 0
    dq = 0
    for (i = 1; i <= length(c); i++) {
      ch = substr(c, i, 1)
      if (sq) { out = out ch; if (ch == q) sq = 0; prev = ch; continue }
      if (ch == "\\") { out = out substr(c, i, 2); prev = substr(c, i + 1, 1); i++; continue }
      if (dq) { out = out ch; if (ch == "\"") dq = 0; prev = ch; continue }
      if (ch == q) { sq = 1; out = out ch; prev = ch; continue }
      if (ch == "\"") { dq = 1; out = out ch; prev = ch; continue }
      if (ch == "#" && (prev == "" || prev == " " || prev == "\t")) break
      out = out ch
      prev = ch
    }
    return out
  }
  function close_paren(s, k,   d, i, ch, sq, dq) {
    d = 0
    sq = 0
    dq = 0
    for (i = k; i <= length(s); i++) {
      ch = substr(s, i, 1)
      if (sq) { if (ch == q) sq = 0; continue }
      if (ch == "\\") { i++; continue }
      if (ch == "`") return 0
      if (dq) {
        if (ch == "\"") dq = 0
        else if (ch == "$" && substr(s, i + 1, 1) == "(") { d++; i++ }
        continue
      }
      if (ch == q) { sq = 1; continue }
      if (ch == "\"") { dq = 1; continue }
      if (ch == "(") d++
      else if (ch == ")") { d--; if (d == 0) return i }
    }
    return 0
  }
  function split_cmds(s,   n, i, ch, two, cur, sq, dq, j) {
    ns = 0
    cur = ""
    sq = 0
    dq = 0
    n = length(s)
    i = 1
    while (i <= n) {
      ch = substr(s, i, 1)
      if (sq) { cur = cur ch; if (ch == q) sq = 0; i++; continue }
      if (ch == "\\") { cur = cur substr(s, i, 2); i += 2; continue }
      if (ch == "$" && substr(s, i + 1, 1) == "(") {
        if (substr(s, i + 2, 1) == "(") return 0
        j = close_paren(s, i + 1)
        if (!j) return 0
        queue[++nq] = substr(s, i + 2, j - i - 2)
        cur = cur substr(s, i, j - i + 1)
        i = j + 1
        continue
      }
      if (ch == "`") return 0
      if (dq) { cur = cur ch; if (ch == "\"") dq = 0; i++; continue }
      if (ch == q) { sq = 1; cur = cur ch; i++; continue }
      if (ch == "\"") { dq = 1; cur = cur ch; i++; continue }
      two = substr(s, i, 2)
      if (two == "&&" || two == "||") { seg[++ns] = cur; segop[ns] = two; cur = ""; i += 2; continue }
      if (ch == ";" || ch == "|" || ch == "&") { seg[++ns] = cur; segop[ns] = ch; cur = ""; i++; continue }
      if (ch == "<" || ch == ">" || ch == "(" || ch == ")") return 0
      cur = cur ch
      i++
    }
    if (sq || dq) return 0
    seg[++ns] = cur
    segop[ns] = "end"
    return 1
  }
  function classify(c,   w, nw, k, cmd, j, path, a, pinned) {
    sub(/^[ \t]+/, "", c)
    sub(/[ \t]+$/, "", c)
    nw = split(c, w, /[ \t]+/)
    if (nw == 0) return "other"
    k = 1
    while (k <= nw && w[k] ~ /^[A-Za-z_][A-Za-z0-9_]*=/) {
      a = w[k]
      sub(/=.*/, "", a)
      if (a !~ /^(CGO_ENABLED|GOOS|GOARCH|GOARM|GOAMD64)$/) return "other"
      k++
    }
    if (k > nw) return "other"
    if (k > 1) {
      if (w[k] == "go" && w[k + 1] == "build") return build_args(w, nw, k + 2)
      return "other"
    }
    if (w[k] == "!") {
      k++
      if (w[k] == "grep" || w[k] == "test") return "read"
      return "other"
    }
    cmd = w[k]
    if (cmd == "grep" || cmd == "test") return "read"
    if (cmd == "true" && nw == k) return "read"
    if (cmd == "go" && w[k + 1] == "build") return build_args(w, nw, k + 2)
    if (cmd == "apk") {
      if (w[k + 1] == "update") {
        for (j = k + 2; j <= nw; j++) if (w[j] !~ /^(-q|--quiet|--no-cache)$/) return "other"
        return "setup"
      }
      if (w[k + 1] == "add") {
        for (j = k + 2; j <= nw; j++) if (w[j] !~ /^(-q|--quiet|--no-cache|--update|-U)$/ && w[j] !~ /^[a-z0-9][a-z0-9._+-]*$/) return "other"
        return "setup"
      }
      return "other"
    }
    if (cmd != "git") return "other"
    if (w[k + 1] == "init") {
      for (j = k + 2; j <= nw; j++) if (w[j] !~ /^(-q|--quiet|\.)$/) return "other"
      return "setup"
    }
    if (w[k + 1] == "remote") {
      if (nw != k + 4 || w[k + 2] != "add" || w[k + 3] != "origin" || w[k + 4] !~ /^https:\/\/[A-Za-z0-9._\/-]+$/) return "other"
      return "setup"
    }
    if (w[k + 1] == "fetch") {
      pinned = 0
      for (j = k + 2; j <= nw; j++) {
        if (w[j] ~ /^"?\$\{?XRAY_COMMIT\}?"?$/) { pinned++; continue }
        if (w[j] !~ /^(-q|--quiet|--no-tags|origin|[0-9]+|--depth|--depth=[0-9]+)$/) return "other"
      }
      if (pinned != 1) return "other"
      return "setup"
    }
    if (w[k + 1] == "checkout") {
      for (j = k + 2; j <= nw; j++) if (w[j] !~ /^(-q|--quiet|--detach|FETCH_HEAD)$/) return "other"
      return "setup"
    }
    if (w[k + 1] != "apply") return "other"
    path = ""
    for (j = k + 2; j <= nw; j++) {
      if (w[j] == "--verbose" || w[j] == "-v") continue
      if (substr(w[j], 1, 1) == "-" || path != "") return "other"
      path = w[j]
    }
    if (path !~ /^\/patches\/[A-Za-z0-9._-]+$/) return "other"
    return "apply:" substr(path, 10)
  }
  function build_args(w, nw, j) {
    last_build_out = ""
    for (; j <= nw; j++) {
      if (w[j] ~ /^-(-)?(overlay|toolexec|modfile|mod|C|pgo|exec)(=|$)/) return "other"
      if (w[j] == "-o") { last_build_out = w[j + 1]; j++; continue }
      if (substr(w[j], 1, 3) == "-o=") last_build_out = substr(w[j], 4)
    }
    return "build"
  }
  function sub_ok(   qi, si, cls) {
    for (qi = 1; qi <= nq; qi++) {
      if (!split_cmds(queue[qi])) return 0
      for (si = 1; si <= ns; si++) {
        if (segop[si] != "&&" && segop[si] != "|" && segop[si] != "end") return 0
        cls = classify(seg[si])
        if (cls == "read") continue
        if (seg[si] ~ /^[ \t]*git[ \t]+rev-parse[ \t]+HEAD[ \t]*$/) continue
        return 0
      }
    }
    return 1
  }
  function run_xray(rest, raw,   si, cls, prev, piped, x, cmd) {
    if (substr(rest, 1, 1) == "[") { taint("xray-builder has an exec-form RUN: " raw); return }
    if (rest ~ /^--/) { taint("xray-builder has a RUN with flags: " raw); return }
    rest = strip_comment(rest)
    nq = 0
    if (!split_cmds(rest)) { taint("xray-builder has a RUN with a redirection, subshell, backquote or unbalanced quote: " raw); return }
    prev = "start"
    for (si = 1; si <= ns; si++) {
      cmd = seg[si]
      sub(/^[ \t]+/, "", cmd)
      sub(/[ \t]+$/, "", cmd)
      if (segop[si] != "&&" && segop[si] != "|" && segop[si] != "end") taint("xray-builder joins a command with " segop[si] ": " cmd)
      piped = (segop[si] == "|" || prev == "|")
      cls = classify(seg[si])
      if (cls != "read" && piped) taint("xray-builder pipes a command that is not a read-only check: " cmd)
      if (substr(cls, 1, 6) == "apply:") {
        x = substr(cls, 7)
        if (built) taint("xray-builder applies a patch after go build: " cmd)
        apply_seen = 1
        if (x == name) {
          if (applied) taint("xray-builder applies the same patch twice: " cmd)
          applied = 1
        }
      } else {
        if (index(seg[si], "/patches")) taint("xray-builder touches /patches outside an exact git apply: " cmd)
        if (cls == "setup" && (apply_seen || built)) taint("xray-builder changes the source after a patch is applied: " cmd)
        if (cls == "build") {
          if (!apply_seen) taint("xray-builder runs go build before any patch is applied: " cmd)
          if (built) taint("xray-builder runs go build more than once: " cmd)
          if (last_build_out !~ /^\/[A-Za-z0-9._\/-]+$/) taint("xray-builder runs go build without an absolute -o path: " cmd)
          build_out = norm(last_build_out)
          built = 1
        }
        if (cls == "other") taint("xray-builder runs a command the check cannot follow: " cmd)
      }
      prev = segop[si]
    }
    if (!sub_ok()) taint("xray-builder runs a command substitution that is not a read-only check: " raw)
  }
  function handle(s,   w, n, i, kw, rest, args, nargs, from_flag, from_val, dest, src, bn, sbn, a, img) {
    sub(/^[ \t]+/, "", s)
    n = split(s, w, /[ \t]+/)
    kw = toupper(w[1])
    rest = substr(s, length(w[1]) + 1)
    sub(/^[ \t]+/, "", rest)
    if (argkey != "") {
      if (kw == "ARG") for (i = 2; i <= n; i++) if (index(w[i], argkey "=") == 1) { argn++; if (argn == 1) argval = substr(w[i], length(argkey) + 2) }
      return
    }
    if ((kw == "RUN" || kw == "COPY" || kw == "ADD") && index(s, "<<")) taint("the Dockerfile uses a heredoc, which the check does not follow: " s)
    if (index(s, "XRAY_EXECUTABLE_PATH")) taint("the Dockerfile names XRAY_EXECUTABLE_PATH: " s)
    if (kw == "FROM") {
      nstage++
      stage = ""
      img = ""
      for (i = 2; i <= n; i++) {
        if (substr(w[i], 1, 2) == "--") continue
        img = tolower(w[i])
        break
      }
      for (i = 2; i < n; i++) if (toupper(w[i]) == "AS") stage = tolower(w[i + 1])
      after_copy = 0
      env_bad = ""
      if (stage == "xray-builder") {
        builders++
        if (builders > 1) taint("the Dockerfile has more than one xray-builder stage")
        if (img in stage_names || index(img, "$")) taint("xray-builder builds on another stage or a variable image: " s)
        else if (img != "scratch" && img !~ /@sha256:[0-9a-f]{64}$/) taint("xray-builder builds on an image that is not pinned by digest: " s)
      }
      if (stage != "") stage_names[stage] = 1
      return
    }
    if (stage != "xray-builder" && after_copy) {
      if (kw == "COPY" || kw == "ADD") {
        dest = w[n]
        if (dest ~ /\/$/ || dest !~ /^\// || index(dest, "$")) taint("the last stage copies into a directory or an unresolved path after it installs xray: " s)
        else {
          dest = norm(dest)
          if (dest == "/" || dest == "/usr" || dest == "/usr/local" || dest == "/usr/local/bin" || dest == "/usr/local/bin/xray") taint("the last stage copies over the installed xray or a directory above it: " s)
        }
      } else if (kw != "ENV" && kw != "ENTRYPOINT" && kw != "CMD" && kw != "LABEL" && kw != "EXPOSE" && kw != "USER" && kw != "WORKDIR" && kw != "HEALTHCHECK" && kw != "STOPSIGNAL") {
        taint("the stage that installs xray runs " kw " after it: " s)
      }
    }
    if (kw == "COPY" || kw == "ADD") {
      nargs = 0
      from_flag = 0
      from_val = ""
      for (i = 2; i <= n; i++) {
        if (substr(w[i], 1, 2) == "--") {
          if (substr(w[i], 1, 7) == "--from=") { from_flag = 1; from_val = tolower(substr(w[i], 8)) }
          else if (w[i] == "--from") { from_flag = 1; from_val = tolower(w[i + 1]); i++ }
          continue
        }
        args[++nargs] = w[i]
      }
      dest = args[nargs]
      src = args[1]
      bn = norm(dest)
      sub(/.*\//, "", bn)
      sbn = src
      sub(/\/+$/, "", sbn)
      sub(/.*\//, "", sbn)
      if (stage != "xray-builder") {
        if (bn == ".env" || sbn == ".env" || (!from_flag && (src == "." || src ~ /\/$/))) env_bad = "the last stage copies an env file or a whole directory of the build context: " s
        if (from_val == "xray-builder") {
          if (nargs != 2 || norm(src) != build_out || norm(dest) != "/usr/local/bin/xray") taint("the image copies something other than the built xray binary from xray-builder to /usr/local/bin/xray: " s)
          else { xray_copied[nstage] = 1; after_copy = 1 }
        } else if (bn == "xray" || sbn == "xray") taint("a stage installs an xray binary that does not come from xray-builder: " s)
        return
      }
      if (dest !~ /^\//) { taint("xray-builder copies to a relative path: " s); return }
      dest = norm(dest)
      if (dest !~ /^\/patches(\/|$)/) { taint("xray-builder copies something outside /patches: " s); return }
      seen_patch_copy = 1
      if (kw == "COPY" && !from_flag && nargs == 2 && src ~ /^patches\/[A-Za-z0-9._-]+$/ && dest == "/" src) {
        if (src == "patches/" name) copied = 1
      } else taint("xray-builder copies something onto /patches other than a patch file: " s)
      return
    }
    if (stage != "xray-builder") {
      if (kw == "RUN" && index(s, "bin/xray")) taint("a RUN outside xray-builder touches bin/xray: " s)
      return
    }
    if (kw == "ARG") {
      a = rest
      sub(/=.*/, "", a)
      if (a !~ /^(TARGET|BUILD)[A-Z]*$|^XRAY_[A-Z0-9_]+$/) taint("xray-builder declares an ARG the check does not allow: " s)
      return
    }
    if (kw == "WORKDIR") { if (seen_patch_copy) taint("xray-builder moves WORKDIR after the patches are copied: " s); return }
    if (kw != "RUN") { taint("xray-builder has a " kw " instruction: " s); return }
    run_xray(rest, s)
  }
  BEGIN { nstage = 0 }
  {
    sub(/\r$/, "")
    if ($0 ~ /[^\t\040-\176]/) taint("the Dockerfile has a byte outside printable ASCII on line " NR)
  }
  /^[ \t]*#[ \t]*[A-Za-z][A-Za-z0-9]*[ \t]*=/ && !seen_instruction { taint("the Dockerfile starts with a parser directive: " $0) }
  /^[ \t]*\/\// && !seen_instruction { taint("the Dockerfile starts with a parser directive: " $0) }
  /^[ \t]*#/ { next }
  /^[ \t]*$/ { next }
  {
    seen_instruction = 1
    line = $0
    continued = sub(/\\[ \t]*$/, "", line)
    inst = (inst == "") ? line : inst " " line
    if (!continued) {
      handle(inst)
      inst = ""
    }
  }
  END {
    if (inst != "") handle(inst)
    if (argkey != "") {
      if (argn > 1) exit 3
      if (argn == 1) print argval
      exit 0
    }
    if (!builders) taint("the Dockerfile has no xray-builder stage")
    if (!xray_copied[nstage]) taint("the last stage does not install xray from xray-builder")
    if (env_bad != "") taint(env_bad)
    if (!built) taint("xray-builder never runs go build after the patches")
    if (explain) {
      if (tainted) print reason
      exit 0
    }
    exit !(applied && copied && !tainted)
  }'

dockerfile_applies_patch() {
  printf '%s\n' "$dockerfile_text" | LC_ALL=C awk -v name="$1" -v q="'" "$dockerfile_program"
}

: >"$work/applied-xray-patches"
: >"$work/xray-patch-headers"
xray_build_note=$(printf '%s\n' "$dockerfile_text" | LC_ALL=C awk -v name= -v explain=1 -v q="'" "$dockerfile_program")
if makefile_applies_xray_patches; then
  [ -z "$xray_build_note" ] ||
    printf 'upstream-drift: xray: no patch header counts, because the Dockerfile'"'"'s xray build is not one the check can follow: %s\n' "$xray_build_note"
  git ls-tree -r "$target_sha" -- patches/ | awk -F'\t' '$1 ~ /^100(644|755) blob / { print $2 }' >"$work/patch-files"
  while IFS= read -r patch_path; do
    case "$patch_path" in
      patches/xray-*.patch) ;;
      *) continue ;;
    esac
    patch_name=${patch_path#patches/}
    dockerfile_applies_patch "$patch_name" || continue
    printf '%s\n' "$patch_path" >>"$work/applied-xray-patches"
    patch_text=$(git show "$target_sha:$patch_path") || die "cannot read $patch_path at $target"
    printf '%s\n' "$patch_text" | awk '/^diff --git / { body = 1 } !body { print }' >>"$work/xray-patch-headers"
  done <"$work/patch-files"
fi
trailer_tokens <"$work/xray-patch-headers" | sort -u - "$work/commit-tokens" >"$work/xray-tokens"
split_tokens "$work/xray-tokens" "-xray"

trailer_scope=""
trailer_ref() {
  awk -v sha="$1" 'index(sha, $0) == 1 { print; exit }' "$work/trailers$trailer_scope"
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
  } | curl --silent --show-error --location --retry 3 --config - --dump-header "$3" --output "$2" --write-out '%{http_code}' "$1"
}

api_fetch() {
  printf '[]\n' >"$1"
  url="$api_url$2&per_page=100"
  pages=0
  while [ -n "$url" ]; do
    pages=$((pages + 1))
    [ "$pages" -le "$max_api_pages" ] ||
      die "GitHub API request $2 still has a next page after $max_api_pages pages; stopping instead of looping"
    : >"$work/page.headers"
    status=$(api_curl "$url" "$work/page.json" "$work/page.headers") ||
      die "GitHub API request $2 failed"
    [ "$status" = 200 ] ||
      die "GitHub API request $2 returned HTTP $status (rate limited? set GITHUB_TOKEN)"
    jq -s '.[0] + .[1]' "$1" "$work/page.json" >"$work/merged.json" ||
      die "GitHub API request $2 did not return a list"
    mv "$work/merged.json" "$1"
    url=$(tr -d '\r' <"$work/page.headers" |
      awk '/^HTTP\// { link = "" } tolower($1) == "link:" { link = $0 } END { print link }' |
      sed -n 's/.*<\([^>]*\)>; *rel="next".*/\1/p')
    case "$url" in
      "" | "$api_url"/*) ;;
      *) die "GitHub API request $2 returned a next link outside $api_url: $url" ;;
    esac
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
    if ! grep -qx "$ghsa" "$work/trailer-ghsa$trailer_scope"; then
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
    elif grep -qx "$ghsa" "$work/trailer-ghsa$trailer_scope"; then
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
  trailer_scope=""
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

  tag_refs=$(git for-each-ref --format='%(refname)' "$node_ns/tags/")
  git rev-list --merges "$target_sha" --not $tag_refs >"$work/target-merges"
  rewritten=0
  rewritten_allowed=0
  while read -r sha <&9; do
    remerge=$(git show --remerge-diff --format= --no-color "$sha") || die "git show --remerge-diff $sha failed"
    [ -n "$remerge" ] || continue
    rewritten=$((rewritten + 1))
    waiver=$(waiver_for "$sha")
    case "$waiver" in
      allowed-merge:*)
        printf '%s\n' "$sha" >>"$work/used"
        status=ALLOWED
        detail=$waiver
        rewritten_allowed=$((rewritten_allowed + 1))
        ;;
      *)
        status=UNPORTED
        detail="a merge in the target history whose result is not what git merges automatically (git show --remerge-diff $(git rev-parse --short=12 "$sha")): it resolved conflicts or dropped one side, as 'git merge -s ours' does. This is checked even when an upstream ref also contains the merge. Review it, then add '$sha allowed-merge: <reason>' to $waiver_file"
        count_unported
        ;;
    esac
    printf '%-8s node rewritten merge %s %s\n' "$status" "$(git rev-parse --short=12 "$sha")" "$(git log -1 --format=%s "$sha")"
    printf '         %s\n' "$detail"
  done 9<"$work/target-merges"
  printf 'upstream-drift: node: %d merges in the target history rewrite the automatic result: %d allowed, %d not allowed\n' \
    "$rewritten" "$rewritten_allowed" "$((rewritten - rewritten_allowed))"

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
    elif [ -n "$waiver" ] && [ -z "$(waiver_branch_scope "$sha")" ] &&
      { only=$(node_branch_only "$sha"); [ -n "$only" ]; }; then
      status=UNPORTED
      detail="branch-only commit: no tag and no release-line branch reaches it, only $only; its waiver depends on that branch staying unmerged, so it must name it with a branch:<ref> token (or the commit must be ported)"
      unported=$((unported + 1))
      count_unported
    elif [ -n "$waiver" ] && { scope=$(waiver_branch_scope "$sha"); [ -n "$scope" ]; } &&
      { outside=$(node_scope_violations "$sha" "$scope"); [ -n "$outside" ]; }; then
      status=UNPORTED
      detail="the waiver scopes this to branch $scope, but the commit is now reachable from $outside; it was merged or released, so review it and either port it or rewrite the waiver without the branch: scope"
      unported=$((unported + 1))
      count_unported
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
    own=$(git show --cc --format= --no-color "$sha") || die "git show --cc $sha failed"
    remerge=$(git show --remerge-diff --format= --no-color "$sha") || die "git show --remerge-diff $sha failed"
    [ -n "$own$remerge" ] || continue
    merges_own=$((merges_own + 1))
    waiver=$(waiver_for "$sha")
    [ -z "$waiver" ] || printf '%s\n' "$sha" >>"$work/used"
    trailer=$(trailer_ref "$sha")
    if [ -n "$trailer" ]; then
      status=PORTED
      detail="referenced by an Upstream/cherry-pick trailer ($trailer)"
    elif [ -n "$waiver" ] && [ -z "$(waiver_branch_scope "$sha")" ] &&
      { only=$(node_branch_only "$sha"); [ -n "$only" ]; }; then
      status=UNPORTED
      detail="branch-only commit: no tag and no release-line branch reaches it, only $only; its waiver depends on that branch staying unmerged, so it must name it with a branch:<ref> token (or the merge must be ported)"
      count_unported
    elif [ -n "$waiver" ] && { scope=$(waiver_branch_scope "$sha"); [ -n "$scope" ]; } &&
      { outside=$(node_scope_violations "$sha" "$scope"); [ -n "$outside" ]; }; then
      status=UNPORTED
      detail="the waiver scopes this to branch $scope, but the merge is now reachable from $outside; review it and either port it or rewrite the waiver without the branch: scope"
      count_unported
    elif [ -n "$waiver" ]; then
      status=WAIVED
      detail=$waiver
      note_pending "$sha" "node merge" "$waiver"
    else
      status=UNPORTED
      detail="upstream merge with changes of its own: its combined diff or its remerge diff is not empty, so it adds changes that none of its parents has or drops one side's changes (git show --cc / --remerge-diff $(git rev-parse --short=12 "$sha")); porting its parents is not enough"
      count_unported
    fi
    printf '%-8s node merge %s %s\n' "$status" "$(git rev-parse --short=12 "$sha")" "$(git log -1 --format=%s "$sha")"
    printf '         %s\n' "$detail"
  done
  printf 'upstream-drift: node: %d upstream merges outside the target, %d with changes of their own\n' "$merges" "$merges_own"
  check_advisories node "$upstream_slug" "$(git show "$target_sha:go.mod" | awk '$1 == "module" && !found { print $2; found = 1 }')" \
    "$(git rev-parse --git-common-dir)" "$work/node-refs" "$target_sha"
}

dockerfile_arg() {
  printf '%s\n' "$dockerfile_text" | LC_ALL=C awk -v argkey="$1" -v q="'" "$dockerfile_program" ||
    die "Dockerfile declares ARG $1 with a value more than once, so the build may use another value than the check reads"
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

tp_branch_only() {
  [ -z "$(tpgit for-each-ref --count=1 --contains "$1" refs/tags/)" ] || return 0
  tpgit for-each-ref --format='%(refname:strip=3)' --contains "$1" refs/remotes/up/ |
    branch_only_heads "$2 main master dev stable testing unstable oldstable"
}

tp_scope_violations() {
  allowed=" $2 "
  { tpgit for-each-ref --format='%(refname:strip=3)' --contains "$1" refs/remotes/up/ |
      awk -v a="$allowed" '$0 !~ /^(renovate|dependabot)\// && index(a, " " $0 " ") == 0 { print "branch " $0 }'
    tpgit for-each-ref --format='%(refname:strip=2)' --contains "$1" refs/tags/ |
      awk '{ print "tag " $0 }'
  } | paste -sd ',' - | sed 's/,/, /g'
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
  trailer_scope=""
  [ "$name" != xray ] || trailer_scope=-xray
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
    elif [ -n "$waiver" ] && [ -z "$(waiver_branch_scope "$sha")" ] &&
      { only=$(tp_branch_only "$sha" "$line"); [ -n "$only" ]; }; then
      status=UNPORTED
      detail="branch-only commit: no tag and no release-line branch reaches it, only $only; its waiver depends on that branch staying unmerged, so it must name it with a branch:<ref> token (or the commit must be ported)"
      unported=$((unported + 1))
      count_unported
    elif [ -n "$waiver" ] && { scope=$(waiver_branch_scope "$sha"); [ -n "$scope" ]; } &&
      { outside=$(tp_scope_violations "$sha" "$scope"); [ -n "$outside" ]; }; then
      status=UNPORTED
      detail="the waiver scopes this to branch $scope, but the commit is now reachable from $outside; review it and either port it or rewrite the waiver without the branch: scope"
      unported=$((unported + 1))
      count_unported
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
  if [ -n "$xray_build_note" ]; then
    printf '%-8s xray build: %s\n' UNPORTED "$xray_build_note"
    printf '         the check cannot follow the xray build in the Dockerfile, so it cannot verify that the image builds the pin with the patches applied; write it in the shape docs/upstream-parity.md describes, or teach the check the new shape\n'
    count_unported
  fi
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
