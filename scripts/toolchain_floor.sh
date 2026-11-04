#!/usr/bin/env bash
set -eu
set -o pipefail
export LC_ALL=C

go_floor=1.26.10
xnet_floor=0.60.0

if [ "$#" -gt 0 ]; then
  root=$1
else
  root=$(git rev-parse --show-toplevel)
fi
cd "$root"

release_re='^[0-9]+\.[0-9]+\.[0-9]+$'
module_release_re='^v[0-9]+\.[0-9]+\.[0-9]+$'
digest_re='@sha256:[0-9a-f]{64}$'
golang_image_re='^(docker\.io/)?(library/)?golang:([^@]+)@'

checked=0
problems=0
refuse() {
  printf 'toolchain-floor: %s\n' "$1" >&2
  problems=$((problems + 1))
}

at_least() {
  awk -v have="$1" -v want="$2" 'BEGIN {
    nh = split(have, h, ".")
    nw = split(want, w, ".")
    n = nh > nw ? nh : nw
    for (i = 1; i <= n; i++) {
      a = (i <= nh ? h[i] : 0) + 0
      b = (i <= nw ? w[i] : 0) + 0
      if (a > b) exit 0
      if (a < b) exit 1
    }
    exit 0
  }'
}

lines() {
  printf '%s\n' "$1" | grep -c . || true
}

require_go() {
  local where=$1 version=$2
  checked=$((checked + 1))
  if ! [[ $version =~ $release_re ]]; then
    refuse "$where names Go '$version', which is not an exact release (X.Y.Z); the floor is $go_floor"
  elif ! at_least "$version" "$go_floor"; then
    refuse "$where names Go $version, below the security floor $go_floor"
  fi
}

require_xnet() {
  local where=$1 version=$2
  checked=$((checked + 1))
  if ! [[ $version =~ $module_release_re ]]; then
    refuse "$where uses golang.org/x/net '$version', which is not a tagged release; the floor is v$xnet_floor"
  elif ! at_least "${version#v}" "$xnet_floor"; then
    refuse "$where uses golang.org/x/net $version, below the security floor v$xnet_floor"
  fi
}

if [ ! -f go.mod ]; then
  refuse "go.mod is missing"
else
  toolchain=$(awk '$1 == "toolchain" { print $2 }' go.mod)
  goline=$(awk '$1 == "go" && NF == 2 { print $2 }' go.mod)
  if [ "$(lines "$toolchain")" -gt 1 ]; then
    refuse "go.mod has more than one toolchain line"
  elif [ -n "$toolchain" ]; then
    require_go "go.mod toolchain" "${toolchain#go}"
  elif [ -n "$goline" ]; then
    require_go "go.mod go line (no toolchain line)" "$goline"
  else
    refuse "go.mod has neither a toolchain line nor a go line"
  fi
  xnet=$(awk '
    $1 == "require" && $2 == "(" { block = 1; next }
    block && $1 == ")" { block = 0; next }
    block && $1 == "golang.org/x/net" { print $2 }
    !block && $1 == "require" && $2 == "golang.org/x/net" { print $3 }
  ' go.mod)
  if [ -z "$xnet" ]; then
    refuse "go.mod does not require golang.org/x/net, so its version cannot be checked against v$xnet_floor"
  elif [ "$(lines "$xnet")" -gt 1 ]; then
    refuse "go.mod requires golang.org/x/net more than once"
  else
    require_xnet "go.mod" "$xnet"
  fi
  if grep -qE '^[[:space:]]*(replace[[:space:]]+)?golang\.org/x/net([[:space:]]+v[^[:space:]]+)?[[:space:]]+=>' go.mod; then
    refuse "go.mod replaces golang.org/x/net, which takes it out of the floor check"
  fi
fi

shopt -s nullglob
workflows=(.github/workflows/*.yml .github/workflows/*.yaml)
dockerfiles=(Dockerfile Dockerfile.*)
glob_patches=(patches/xray-*.patch)
shopt -u nullglob

for workflow in "${workflows[@]}"; do
  name=${workflow#.github/workflows/}
  setups=$(grep -cE '^[[:space:]]*(-[[:space:]]+)?uses:[[:space:]]*["'\'']?actions/setup-go@' "$workflow" || true)
  if grep -qE '^[[:space:]]*go-version-file:' "$workflow"; then
    refuse "$name sets up Go from go-version-file; pin an exact go-version at or above $go_floor"
  fi
  versions=$(grep -E '^[[:space:]]*go-version:' "$workflow" | sed -E 's/^[[:space:]]*go-version:[[:space:]]*//; s/[[:space:]]+#.*$//; s/^["'\'']//; s/["'\'']$//' || true)
  count=$(lines "$versions")
  if [ "$count" != "$setups" ]; then
    refuse "$name has $setups actions/setup-go steps but $count go-version pins; every setup-go step needs an exact go-version"
  fi
  while IFS= read -r version; do
    [ -n "$version" ] || continue
    require_go "$name go-version" "$version"
  done <<EOF
$versions
EOF
done

golang_stages=0
for dockerfile in "${dockerfiles[@]}"; do
  [ -f "$dockerfile" ] || continue
  while IFS= read -r image; do
    [ -n "$image" ] || continue
    case "$image" in
      golang:* | library/golang:* | docker.io/library/golang:* | docker.io/golang:*) ;;
      \$*)
        refuse "$dockerfile builds from $image, an image chosen by a build argument, so its Go version cannot be checked"
        checked=$((checked + 1))
        continue
        ;;
      *) continue ;;
    esac
    golang_stages=$((golang_stages + 1))
    if ! [[ $image =~ $digest_re ]]; then
      checked=$((checked + 1))
      refuse "$dockerfile builds from $image without an @sha256 digest"
      continue
    fi
    if ! [[ $image =~ $golang_image_re ]]; then
      checked=$((checked + 1))
      refuse "$dockerfile builds from $image, which is not a golang:<version> image"
      continue
    fi
    tag=${BASH_REMATCH[3]}
    require_go "$dockerfile image golang:$tag" "${tag%%-*}"
  done <<EOF
$(awk 'toupper($1) == "FROM" { for (i = 2; i <= NF; i++) if ($i !~ /^--/) { print $i; break } }' "$dockerfile")
EOF
done
if [ "$golang_stages" = 0 ]; then
  refuse "no Dockerfile builds from a golang image, so the toolchain the image ships cannot be checked"
fi

patch_xnet() {
  awk '
    /^diff --git / { gomod = ($0 == "diff --git a/go.mod b/go.mod"); next }
    gomod && /^\+[[:space:]]+golang\.org\/x\/net[[:space:]]+v/ { sub(/^\+[[:space:]]+golang\.org\/x\/net[[:space:]]+/, ""); sub(/[[:space:]].*$/, ""); print }
  ' "$1"
}

final=
final_xnet() {
  local patch version
  final=
  for patch in "$@"; do
    if [ ! -f "$patch" ]; then
      refuse "$patch is applied to the Xray build but is missing"
      continue
    fi
    version=$(patch_xnet "$patch")
    if [ "$(lines "$version")" -gt 1 ]; then
      refuse "$patch sets golang.org/x/net in go.mod more than once"
      continue
    fi
    [ -z "$version" ] || final=$version
  done
}

xray_stage=
xray_stage_found=0
if [ -f Dockerfile ] && xray_stage=$(awk '
  tolower($1) == "from" {
    in_stage = (NF >= 3 && tolower($(NF - 1)) == "as" && tolower($NF) == "xray-builder")
    if (in_stage) found = 1
  }
  in_stage { print }
  END { exit found ? 0 : 1 }
' Dockerfile); then
  xray_stage_found=1
fi

xray_chain() {
  awk '
    function trim(text) { sub(/^[[:space:]]+/, "", text); sub(/[[:space:]]+$/, "", text); return text }
    function run(body,   i, c, quote, cur, size, n, unsafe, parts, depth, tick) {
      quote = 0; cur = ""; n = 0; unsafe = 0; size = length(body); depth = 0; tick = 0
      for (i = 1; i <= size; i++) {
        c = substr(body, i, 1)
        if (quote == 1) {
          cur = cur c
          if (c == "\\" && i < size) { i++; cur = cur substr(body, i, 1) }
          else if (c == "\"") quote = 0
          continue
        }
        if (quote == 2) { cur = cur c; if (c == "\047") quote = 0; continue }
        if (c == "\"") { quote = 1; cur = cur c; continue }
        if (c == "\047") { quote = 2; cur = cur c; continue }
        if (c == "\\" && i < size) { cur = cur c substr(body, i + 1, 1); i++; continue }
        if (c == "`") tick = !tick
        if (c == "(") depth++
        if (c == ")") { if (depth > 0) depth--; else unsafe = 1 }
        if (c == "&" && substr(body, i + 1, 1) == "&" && depth == 0 && !tick) { parts[++n] = trim(cur); cur = ""; i++; continue }
        if (c == "|" && substr(body, i + 1, 1) == "|") unsafe = 1
        if (c == ";" || c == "&") unsafe = 1
        cur = cur c
      }
      if (quote != 0 || depth != 0 || tick) unsafe = 1
      parts[++n] = trim(cur)
      for (i = 1; i <= n; i++) printf "%s\t%s\n", (unsafe ? "unsafe" : "safe"), parts[i]
    }
    function logical(line,   word) {
      word = line
      sub(/^[[:space:]]+/, "", word)
      if (toupper(substr(word, 1, 4)) != "RUN " && toupper(substr(word, 1, 4)) != "RUN\t") return
      word = trim(substr(word, 5))
      if (substr(word, 1, 1) == "[") return
      run(word)
    }
    {
      line = $0
      if (line ~ /\\[[:space:]]*$/) {
        sub(/\\[[:space:]]*$/, "", line)
        buffer = buffer line " "
        next
      }
      logical(buffer line)
      buffer = ""
    }
    END { if (buffer != "") logical(buffer) }
  '
}

xnet_check_re='^grep -q "golang\.org/x/net (v[^"$[:space:]]+)( // indirect)?\$" go\.mod$'
apply_re='git apply[^&]*/patches/[A-Za-z0-9._-]+\.patch'

upstream_xnet=
if [ "$xray_stage_found" = 0 ]; then
  checked=$((checked + 1))
  refuse "the Dockerfile has no xray-builder stage, so the Xray binary's x/net cannot be checked against v$xnet_floor"
else
  docker_xnet=
  docker_source=
  while IFS=$'\t' read -r safety element; do
    [ -n "$element" ] || continue
    applied=$(printf '%s\n' "$element" | grep -oE "$apply_re" | sed -E 's@.*/patches/@@' || true)
    if [ -n "$applied" ]; then
      while IFS= read -r patch; do
        [ -n "$patch" ] || continue
        final_xnet "patches/$patch"
        if [ -n "$final" ]; then
          docker_xnet=$final
          docker_source="patch order"
        fi
      done <<EOF
$applied
EOF
      continue
    fi
    [ "$safety" = safe ] || continue
    [[ $element =~ $xnet_check_re ]] || continue
    asserted=${BASH_REMATCH[1]}
    if [ -z "$docker_xnet" ]; then
      docker_xnet=$asserted
      docker_source="go.mod check"
      upstream_xnet=$asserted
    elif [ "$asserted" != "$docker_xnet" ]; then
      checked=$((checked + 1))
      refuse "the Dockerfile xray-builder stage checks go.mod for golang.org/x/net $asserted, but its $docker_source leaves $docker_xnet there"
    fi
  done <<EOF
$(printf '%s\n' "$xray_stage" | xray_chain)
EOF
  if [ -z "$docker_xnet" ]; then
    checked=$((checked + 1))
    refuse "neither a patch applied by the Dockerfile xray-builder stage nor a go.mod check in it sets golang.org/x/net, so the Xray binary's x/net cannot be checked against v$xnet_floor"
  elif [ "$docker_source" = "patch order" ]; then
    require_xnet "the Xray binary (Dockerfile patch order)" "$docker_xnet"
  else
    require_xnet "the Xray binary (Dockerfile xray-builder go.mod check)" "$docker_xnet"
  fi
fi

make_commit_line="XRAY_COMMIT ?= \$(shell sed -n 's/^ARG XRAY_COMMIT=//p' Dockerfile)"
make_builds_docker_commit=0
if [ -n "$upstream_xnet" ] && [ -f Makefile ] \
  && [ "$(grep -cE '^[[:space:]]*(export[[:space:]]+|override[[:space:]]+)*XRAY_COMMIT[[:space:]]*(\?|:|::|:::|\+|!)?=' Makefile || true)" = 1 ] \
  && grep -qxF "$make_commit_line" Makefile \
  && [ "$(grep -c '^ARG XRAY_COMMIT=' Dockerfile || true)" = 1 ] \
  && printf '%s\n' "$xray_stage" | grep -q '^ARG XRAY_COMMIT='; then
  make_builds_docker_commit=1
fi

final=
[ "${#glob_patches[@]}" = 0 ] || final_xnet "${glob_patches[@]}"
if [ -n "$final" ]; then
  require_xnet "the Xray binary (make build_xray patch order)" "$final"
elif [ "$make_builds_docker_commit" = 1 ]; then
  require_xnet "the Xray binary (make build_xray, upstream go.mod at the Dockerfile XRAY_COMMIT)" "$upstream_xnet"
else
  checked=$((checked + 1))
  refuse "no patches/xray-*.patch sets golang.org/x/net and make build_xray has no upstream go.mod value (it must build the one Dockerfile XRAY_COMMIT whose go.mod the xray-builder stage checks), so the make build_xray binary's x/net cannot be checked against v$xnet_floor"
fi

if [ "$problems" -gt 0 ]; then
  printf 'toolchain-floor: %d problems in %d pins; the floor is Go %s and golang.org/x/net v%s\n' "$problems" "$checked" "$go_floor" "$xnet_floor" >&2
  exit 1
fi
printf 'toolchain-floor: %d pins checked, all at or above Go %s and golang.org/x/net v%s\n' "$checked" "$go_floor" "$xnet_floor"
