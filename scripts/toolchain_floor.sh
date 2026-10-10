#!/usr/bin/env bash
set -eu
set -o pipefail
export LC_ALL=C

go_floor=1.26.9
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

if [ -f Dockerfile ] && grep -q 'AS xray-builder' Dockerfile; then
  docker_patches=()
  while IFS= read -r patch; do
    [ -n "$patch" ] || continue
    docker_patches+=("patches/$patch")
  done <<EOF
$(grep -oE 'git apply[^&]*/patches/[A-Za-z0-9._-]+\.patch' Dockerfile | sed -E 's@.*/patches/@@')
EOF
  final=
  [ "${#docker_patches[@]}" = 0 ] || final_xnet "${docker_patches[@]}"
  if [ -z "$final" ]; then
    refuse "no patch applied by the Dockerfile xray-builder stage sets golang.org/x/net, so the Xray binary's x/net cannot be checked against v$xnet_floor"
  else
    require_xnet "the Xray binary (Dockerfile patch order)" "$final"
  fi
  final=
  [ "${#glob_patches[@]}" = 0 ] || final_xnet "${glob_patches[@]}"
  if [ -z "$final" ]; then
    refuse "no patches/xray-*.patch sets golang.org/x/net, so the make build_xray binary's x/net cannot be checked against v$xnet_floor"
  else
    require_xnet "the Xray binary (make build_xray patch order)" "$final"
  fi
fi

if [ "$problems" -gt 0 ]; then
  printf 'toolchain-floor: %d problems in %d pins; the floor is Go %s and golang.org/x/net v%s\n' "$problems" "$checked" "$go_floor" "$xnet_floor" >&2
  exit 1
fi
printf 'toolchain-floor: %d pins checked, all at or above Go %s and golang.org/x/net v%s\n' "$checked" "$go_floor" "$xnet_floor"
