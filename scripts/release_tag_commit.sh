#!/usr/bin/env bash
set -eu
set -o pipefail

tag=${1:?usage: release_tag_commit.sh <tag>}
: "${GITHUB_REPOSITORY:?GITHUB_REPOSITORY is not set}"

object=$(gh api "repos/${GITHUB_REPOSITORY}/git/ref/tags/${tag}")
type=$(jq -r .object.type <<< "${object}")
sha=$(jq -r .object.sha <<< "${object}")
for _ in 1 2 3 4 5; do
  [ "${type}" = tag ] || break
  object=$(gh api "repos/${GITHUB_REPOSITORY}/git/tags/${sha}")
  type=$(jq -r .object.type <<< "${object}")
  sha=$(jq -r .object.sha <<< "${object}")
done
printf '%s %s\n' "${type}" "${sha}"
