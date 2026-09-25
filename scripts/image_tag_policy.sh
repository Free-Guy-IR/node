#!/usr/bin/env bash
set -eu
set -o pipefail

api_url=${GITHUB_API_URL:-https://api.github.com}
repo=${GITHUB_REPOSITORY:-}
tag_re='^[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}$'
release_re='^v[0-9]'

die() {
  echo "::error title=Image tag check failed::$*"
  exit 2
}

[ -n "$repo" ] || die "GITHUB_REPOSITORY is not set"
[ -n "${GH_TOKEN:-}" ] || die "GH_TOKEN is not set"
[ "$#" -gt 0 ] || die "no image tags to check"

api_status() {
  curl -sS -o /dev/null -w '%{http_code}' \
    -H "Authorization: Bearer ${GH_TOKEN}" \
    -H "Accept: application/vnd.github+json" \
    "${api_url}/repos/${repo}/$1" || printf '000'
}

refused=0
refuse() {
  echo "::error title=Image tag refused::$1: $2"
  refused=$((refused + 1))
}

for tag in "$@"; do
  if ! [[ $tag =~ $tag_re ]]; then
    refuse "$tag" "not a valid image tag"
    continue
  fi
  if [ "$tag" = latest ]; then
    refuse "$tag" "latest is moved only by Publish Release, for a non-prerelease release"
    continue
  fi
  if [[ $tag =~ $release_re ]]; then
    refuse "$tag" "it looks like a release tag (^v[0-9]); release image tags are immutable and come only from Publish Release"
    continue
  fi
  for path in "releases/tags/$tag" "git/ref/tags/$tag"; do
    status=$(api_status "$path")
    case "$status" in
      200)
        refuse "$tag" "the repository already has a release or tag named $tag; release image tags are immutable and come only from Publish Release"
        break
        ;;
      404) ;;
      *) die "GitHub returned HTTP $status for $path, so $tag cannot be checked" ;;
    esac
  done
done

if [ "$refused" -gt 0 ]; then
  exit 1
fi
echo "image tags allowed: $*"
