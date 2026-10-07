#!/usr/bin/env bash
set -eu
set -o pipefail

die() {
  printf 'workflow-policy: %s\n' "$*" >&2
  exit 2
}

repo_root=$(git rev-parse --show-toplevel) || die "not inside a git work tree"
cd "$repo_root"

actionlint=${ACTIONLINT:-actionlint}
shellcheck=${SHELLCHECK:-shellcheck}
remote=${WORKFLOW_POLICY_REMOTE:-1}
git_base=${WORKFLOW_POLICY_GIT_BASE:-https://github.com}
workflow_dir=${WORKFLOW_DIR:-.github/workflows}

actionlint_path=$(command -v "$actionlint") || die "actionlint is required (set ACTIONLINT to its path)"
shellcheck_path=$(command -v "$shellcheck") || die "shellcheck is required (set SHELLCHECK to its path)"

shopt -s nullglob
files=("$workflow_dir"/*.yml "$workflow_dir"/*.yaml)
shopt -u nullglob
[ "${#files[@]}" -gt 0 ] || die "no workflow files in $workflow_dir"

failures=0
fail() {
  printf 'FAIL     %s\n' "$*"
  failures=$((failures + 1))
}

if ! "$actionlint_path" -shellcheck "$shellcheck_path" "${files[@]}"; then
  fail "actionlint reported the findings above"
fi

retired_paths="docker-build.yml docker-build-dev.yml release.yml"
for file in "${files[@]}"; do
  case " $retired_paths " in
    *" ${file##*/} "*)
      fail "$file: ${file##*/} is a retired workflow path. Old tags and branches carry an ungated workflow at this path, so its workflow id is disabled in the repository settings and a file here never runs; use image-branch.yml, image-manual.yml or publish-release.yml"
      ;;
  esac
done

sha_re='^[0-9a-f]{40}$'
version_re='^v[0-9]+(\.[0-9]+){0,2}$'
action_re='^([A-Za-z0-9_.-]+)/([A-Za-z0-9_.-]+)(/[^@]*)?@(.+)$'
docker_re='^docker://[^@]+@sha256:[0-9a-f]{64}$'

work=$(mktemp -d "${TMPDIR:-/tmp}/workflow-policy.XXXXXX")
trap 'rm -rf "$work"' EXIT
: >"$work/checked"

ref_kind() {
  local url=$1 ref=$2 listing attempt
  for attempt in 1 2 3; do
    if listing=$(GIT_TERMINAL_PROMPT=0 git ls-remote "$url" "refs/tags/$ref" "refs/heads/$ref" 2>/dev/null); then
      if printf '%s\n' "$listing" | awk -v ref="refs/heads/$ref" '$2 == ref { found = 1 } END { exit !found }'; then
        echo branch
      elif printf '%s\n' "$listing" | awk -v ref="refs/tags/$ref" '$2 == ref { found = 1 } END { exit !found }'; then
        echo tag
      else
        echo missing
      fi
      return 0
    fi
    [ "$attempt" = 3 ] || sleep "$attempt"
  done
  echo unreadable
}

check_uses() {
  local where=$1 value=$2 owner repo ref key kind
  case "$value" in
    ./*)
      printf 'ok       %s %s (local)\n' "$where" "$value"
      return 0
      ;;
    docker://*)
      if [[ $value =~ $docker_re ]]; then
        printf 'ok       %s %s\n' "$where" "$value"
      else
        fail "$where: $value is a container image without an @sha256 digest"
      fi
      return 0
      ;;
  esac
  if ! [[ $value =~ $action_re ]]; then
    fail "$where: '$value' is not ./local, docker://image@sha256 or owner/repo@ref"
    return 0
  fi
  owner=${BASH_REMATCH[1]}
  repo=${BASH_REMATCH[2]}
  ref=${BASH_REMATCH[4]}
  if [[ $ref =~ $sha_re ]]; then
    printf 'ok       %s %s (full commit sha)\n' "$where" "$value"
    return 0
  fi
  if ! [[ $ref =~ $version_re ]]; then
    fail "$where: $value is not pinned to a version tag (vN, vN.N or vN.N.N) or a full commit sha"
    return 0
  fi
  if [ "$remote" != 1 ]; then
    printf 'ok       %s %s (version tag, remote check skipped)\n' "$where" "$value"
    return 0
  fi
  key="$owner/$repo@$ref"
  kind=$(awk -v key="$key" '$1 == key { print $2; exit }' "$work/checked")
  if [ -z "$kind" ]; then
    kind=$(ref_kind "$git_base/$owner/$repo.git" "$ref")
    printf '%s %s\n' "$key" "$kind" >>"$work/checked"
  fi
  case "$kind" in
    tag) printf 'ok       %s %s (tag in %s/%s)\n' "$where" "$value" "$owner" "$repo" ;;
    branch) fail "$where: $value floats on a branch: $owner/$repo has a branch named $ref" ;;
    missing) fail "$where: $value names no tag in $owner/$repo" ;;
    *) fail "$where: cannot read the refs of $owner/$repo to verify $ref" ;;
  esac
}

for file in "${files[@]}"; do
  if ! grep -Eq '^permissions:' "$file"; then
    fail "$file: no top-level permissions block"
  fi
  while IFS= read -r match; do
    line_no=${match%%:*}
    value=${match#*:}
    value=$(printf '%s\n' "$value" | sed -E \
      -e 's/^[[:space:]]*(-[[:space:]]+)?uses:[[:space:]]*//' \
      -e 's/[[:space:]]+#.*$//' \
      -e 's/[[:space:]]+$//' \
      -e "s/^[\"'](.*)[\"']\$/\\1/")
    check_uses "$file:$line_no" "$value"
  done < <(grep -nE '^[[:space:]]*(-[[:space:]]+)?uses:[[:space:]]*[^[:space:]]' "$file" || true)
done

if [ "$failures" -gt 0 ]; then
  printf 'workflow-policy: %d findings in %d workflow files\n' "$failures" "${#files[@]}"
  exit 1
fi
printf 'workflow-policy: %d workflow files pass actionlint, the pin convention and the permissions rule\n' "${#files[@]}"
