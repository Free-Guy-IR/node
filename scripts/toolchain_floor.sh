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

xray_stages=0
[ ! -f Dockerfile ] || xray_stages=$(awk 'tolower($1) == "from" && NF >= 3 && tolower($(NF - 1)) == "as" && tolower($NF) == "xray-builder" { n++ } END { print n + 0 }' Dockerfile)
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
    function run(body,   i, c, cur, size, n, unsafe, parts, d, kind, q) {
      cur = ""; n = 0; unsafe = 0; size = length(body); d = 0; kind[0] = ""; q[0] = 0
      for (i = 1; i <= size; i++) {
        c = substr(body, i, 1)
        if (q[d] == 2) { cur = cur c; if (c == "\047") q[d] = 0; continue }
        if (q[d] == 1) {
          cur = cur c
          if (c == "\\" && i < size) { i++; cur = cur substr(body, i, 1) }
          else if (c == "\"") q[d] = 0
          else if (c == "$" && substr(body, i + 1, 1) == "(") { i++; cur = cur "("; kind[++d] = "("; q[d] = 0 }
          else if (c == "`") { kind[++d] = "`"; q[d] = 0 }
          continue
        }
        if (c == "\\" && i < size) { cur = cur c substr(body, i + 1, 1); i++; continue }
        if (c == "\"") { q[d] = 1; cur = cur c; continue }
        if (c == "\047") { q[d] = 2; cur = cur c; continue }
        if (c == "`") {
          if (d > 0 && kind[d] == "`") d--
          else { kind[++d] = "`"; q[d] = 0 }
          cur = cur c
          continue
        }
        if (c == "(") { kind[++d] = "("; q[d] = 0; cur = cur c; continue }
        if (c == ")") {
          if (d > 0 && kind[d] == "(") d--
          else unsafe = 1
          cur = cur c
          continue
        }
        if (c == "&" && substr(body, i + 1, 1) == "&" && d == 0) { parts[++n] = trim(cur); cur = ""; i++; continue }
        if (c == "|" && substr(body, i + 1, 1) == "|") unsafe = 1
        if (c == ";" || c == "&") unsafe = 1
        cur = cur c
      }
      if (q[d] != 0 || d != 0) unsafe = 1
      parts[++n] = trim(cur)
      for (i = 1; i <= n; i++) printf "%s\t%s\n", (unsafe ? "unsafe" : "safe"), parts[i]
    }
    function logical(line,   word) {
      word = trim(line)
      if (word == "" || substr(word, 1, 1) == "#") return
      if (toupper(substr(word, 1, 4)) != "RUN " && toupper(substr(word, 1, 4)) != "RUN\t") { printf "inst\t%s\n", word; return }
      word = trim(substr(word, 5))
      if (substr(word, 1, 1) == "[") { printf "exec\t%s\n", word; return }
      if (index(word, "<<")) { printf "heredoc\t%s\n", word; return }
      run(word)
    }
    {
      line = $0
      if (line ~ /^[[:space:]]*#/) next
      if (line ~ /\\[[:space:]]*$/) {
        sub(/\\[[:space:]]*$/, "", line)
        buffer = buffer line
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
foreign_apply_re='(^|[^[:alnum:]_.-])(git[[:space:]]+(apply|am)|patch)([[:space:]]|$)'
gomod_name_re='go\.(mod|work)'
toolchain_env_re='GOTOOLCHAIN|GOENV'
patch_copy_re='^(COPY|ADD)[[:space:]]+([^[:space:]]+[[:space:]]+)+/patches/[A-Za-z0-9._-]+$'
gomod_tool_re='(^|[^[:alnum:]_.-])go[[:space:]]+(get|work|mod[[:space:]]+(edit|tidy|init))([[:space:]]|$)'
chdir_re='(^|[^[:alnum:]_./-])(cd|pushd|popd|chdir)([[:space:]]|$)|(^|[[:space:]])(-C|--chdir|--directory)([[:space:]=]|$)|-modfile|GOFLAGS|GOWORK'
gomod_write_re='(^|[^[:alnum:]_.-])(sed|perl|ruby|python3?|ed|ex|vi|vim|awk|gawk|cp|mv|tee|install|ln|dd|truncate|rsync|touch|rm|unlink)[[:space:]][^&|;]*go\.(mod|work)'
redirect_re='>[>|]?[[:space:]]*["'\'']?[^[:space:]&|;]*go\.(mod|work)'
git_move_re='(^|[^[:alnum:]_.-])git[[:space:]]+(-[^[:space:]]+[[:space:]]+)*(checkout|reset|switch|restore|pull|merge|rebase|cherry-pick|am|fetch|stash|clone|read-tree|update-index|worktree|submodule)([[:space:]]|$)'
go_build_token_re='^[[:space:]]+("[^"\\$`'"'"']*"|[^[:space:]"\\$`'"'"']+)([[:space:]].*)?$'
assign_word_re='^[[:space:]]+([^[:space:]]+)([[:space:]].*)?$'
final_package_re='^[a-z0-9][a-z0-9+_-]*$'
final_command_re='^[a-z0-9._+-]+$'
final_path_re='^/[A-Za-z0-9._/+-]+$'
git_apply_args_re='^(--?[a-z][a-z-]*[[:space:]]+)*/patches/[A-Za-z0-9._-]+\.patch$'
export_names_re='^(export|readonly|declare|typeset|local|env|set)$'

command_words() {
  awk '
    function close_sub(s, i,   n, c, d, q, kind) {
      n = length(s); d = 1; q[1] = 0; kind[1] = "("
      for (; i <= n; i++) {
        c = substr(s, i, 1)
        if (q[d] == 2) { if (c == "\047") q[d] = 0; continue }
        if (c == "\\") { i++; continue }
        if (c == "$" && substr(s, i + 1, 1) == "(") { i++; kind[++d] = "("; q[d] = 0; continue }
        if (c == "`") return 0
        if (q[d] == 1) { if (c == "\"") q[d] = 0; continue }
        if (c == "\"") { q[d] = 1; continue }
        if (c == "\047") { q[d] = 2; continue }
        if (c == "(") { kind[++d] = "("; q[d] = 0; continue }
        if (c == ")") { d--; if (d == 0) return i }
      }
      return 0
    }
    function word_end() { if (inword) { words[++nw] = w; w = ""; inword = 0 } }
    function command_end(   i, j, rest, assigns) {
      word_end()
      i = 1
      assigns = ""
      while (i <= nw && (words[i] == "!" || words[i] ~ /^[A-Za-z_][A-Za-z0-9_]*=/)) { assigns = assigns (assigns == "" ? "" : " ") words[i]; i++ }
      if (i <= nw) {
        rest = ""
        for (j = i + 2; j <= nw; j++) rest = rest (rest == "" ? "" : " ") words[j]
        printf "%s\037%s\037%s\037%s\n", words[i], (i < nw ? words[i + 1] : ""), rest, assigns
      }
      nw = 0
    }
    function scan(s,   n, i, c, q, j) {
      n = length(s); q = 0; nw = 0; w = ""; inword = 0
      for (i = 1; i <= n; i++) {
        c = substr(s, i, 1)
        if (q == 2) { w = w c; if (c == "\047") q = 0; continue }
        if (c == "\\") { w = w c substr(s, i + 1, 1); i++; inword = 1; continue }
        if (c == "`") { printf "`\037\037\037\n"; w = w c; inword = 1; continue }
        if (c == "$" && substr(s, i + 1, 1) == "(") {
          j = close_sub(s, i + 2)
          if (j == 0) { printf "$(\037\037\037\n"; return }
          queue[++qn] = substr(s, i + 2, j - i - 2)
          w = w substr(s, i, j - i + 1); inword = 1; i = j
          continue
        }
        if (q == 1) { w = w c; if (c == "\"") q = 0; continue }
        if (c == "$" && (substr(s, i + 1, 1) == "\047" || substr(s, i + 1, 1) == "\"")) { printf "$%s\037\037\037\n", substr(s, i + 1, 1); w = w c; inword = 1; continue }
        if (c == "\"" || c == "\047") { q = (c == "\"" ? 1 : 2); w = w c; inword = 1; continue }
        if (c == " " || c == "\t") { word_end(); continue }
        if (c == "|" || c == "&" || c == ";" || c == "(" || c == ")") { command_end(); continue }
        if (c == ">" || c == "<") { command_end(); printf "%s\037\037\037\n", c; continue }
        w = w c; inword = 1
      }
      if (q != 0) printf "%s\037\037\037\n", "$unbalanced"
      command_end()
    }
    { qn = 1; queue[1] = $0; for (qi = 1; qi <= qn; qi++) scan(queue[qi]) }
  '
}

go_build_allowed() {
  local rest=" $1" assigns=" $2" token want= package=0
  while [[ $assigns =~ $assign_word_re ]]; do
    token=${BASH_REMATCH[1]}
    assigns=${BASH_REMATCH[2]}
    case "$token" in
      'CGO_ENABLED=0' | 'GOOS=${TARGETOS}' | 'GOARCH=${TARGETARCH}') ;;
      *) return 1 ;;
    esac
  done
  [[ $assigns =~ ^[[:space:]]*$ ]] || return 1
  while [[ $rest =~ $go_build_token_re ]]; do
    token=${BASH_REMATCH[1]}
    rest=${BASH_REMATCH[2]}
    [ "$package" = 0 ] || return 1
    if [ -n "$want" ]; then
      [ "$token" = "$want" ] || return 1
      want=
      continue
    fi
    case "$token" in
      -trimpath) ;;
      -ldflags) want='"-s -w -buildid="' ;;
      -o) want=/out/xray ;;
      ./main) package=1 ;;
      *) return 1 ;;
    esac
  done
  [ -z "$rest" ] && [ -z "$want" ] && [ "$package" = 1 ]
}

allowed_after_check() {
  local name=$1 sub=$2 rest=$3 assigns=$4
  case "$name" in
    grep | test) return 0 ;;
    go) [ "$sub" = build ] && go_build_allowed "$rest" "$assigns" ;;
    git) [ "$sub" = apply ] && [[ $rest =~ $git_apply_args_re ]] ;;
    *) return 1 ;;
  esac
}

toolchain_kept_local() {
  local rest
  rest=$(printf '%s\n' "$1" | sed -E 's/(^|[[:space:]])GOTOOLCHAIN=local([[:space:]]|$)/\1\2/g')
  ! [[ $rest =~ $toolchain_env_re ]]
}

element_forms() {
  scan=$2
  [ "$1" != exec ] || scan=$(printf '%s\n' "$2" | tr '",[]' '    ')
  bare=$(printf '%s\n' "$scan" | sed -E 's/\$\{[^}]*\}//g' | tr -d "\"'\\\\")
}

check_toolchain_env() {
  if ! toolchain_kept_local "$scan" || ! toolchain_kept_local "$bare"; then
    checked=$((checked + 1))
    refuse "the Dockerfile $1 stage runs '${2:0:120}', which sets GOTOOLCHAIN to something other than local or sets GOENV, so the build need not use the image's Go"
  fi
  if [[ $scan == *XRAY_EXECUTABLE_PATH* ]] || [[ $bare == *XRAY_EXECUTABLE_PATH* ]]; then
    checked=$((checked + 1))
    refuse "the Dockerfile $1 stage runs '${2:0:120}', which sets XRAY_EXECUTABLE_PATH, so the node need not run the /usr/local/bin/xray the xray-builder stage checks"
  fi
  if [[ $scan == *GOFLAGS* ]] || [[ $bare == *GOFLAGS* ]]; then
    checked=$((checked + 1))
    refuse "the Dockerfile $1 stage runs '${2:0:120}', which sets GOFLAGS, so the build need not use the flags and go.mod the guard checks"
  fi
}

final_stage_run_allowed() {
  local name sub rest assigns word words
  case "$1" in heredoc | exec) return 1 ;; esac
  [[ $2 != *[\$\`\\\"\']* ]] || return 1
  while IFS=$'\037' read -r name sub rest assigns; do
    [ -n "$name" ] || continue
    [ -z "$assigns" ] || return 1
    if [ "$name" = "{" ]; then
      name=$sub
      if [[ $rest == *" "* ]]; then
        sub=${rest%% *}
        rest=${rest#* }
      else
        sub=$rest
        rest=
      fi
    fi
    case "$name" in
      apk)
        case "$sub" in
          update) [ -z "$rest" ] || return 1 ;;
          add)
            read -r -a words <<<"$rest"
            for word in "${words[@]}"; do
              [ "$word" = --no-cache ] || [[ $word =~ $final_package_re ]] || return 1
            done
            ;;
          *) return 1 ;;
        esac
        ;;
      command) [ "$sub" = -v ] && [[ $rest =~ $final_command_re ]] || return 1 ;;
      test)
        { [ "$sub" = -x ] || [ "$sub" = -f ]; } && [[ $rest =~ $final_path_re ]] && [[ $rest != *..* ]] \
          && [[ $rest != /usr/local && $rest != /usr/local/* ]] || return 1
        ;;
      '>' | /dev/null | '}') [ -z "$sub" ] && [ -z "$rest" ] || return 1 ;;
      *) return 1 ;;
    esac
  done <<EOF
$(printf '%s\n' "$2" | command_words)
EOF
}

dockerfile_chain=
[ ! -f Dockerfile ] || dockerfile_chain=$(xray_chain <Dockerfile)

upstream_xnet=
if [ "$xray_stage_found" = 0 ]; then
  checked=$((checked + 1))
  refuse "the Dockerfile has no xray-builder stage, so the Xray binary's x/net cannot be checked against v$xnet_floor"
elif [ "$xray_stages" != 1 ]; then
  checked=$((checked + 1))
  refuse "the Dockerfile has $xray_stages xray-builder stages, so the one the image copies from cannot be told apart"
else
  docker_xnet=
  docker_source=
  check_seen=0
  workdirs=0
  while IFS=$'\t' read -r safety element; do
    [ -n "$element" ] || continue
    element_forms "$safety" "$element"
    shown=${element:0:120}
    check_toolchain_env xray-builder "$element"
    if [ "$safety" = heredoc ]; then
      checked=$((checked + 1))
      refuse "the Dockerfile xray-builder stage has a heredoc RUN ('$shown'), whose commands cannot be checked for go.mod changes"
      continue
    fi
    if [ "$safety" = inst ]; then
      word=$(printf '%s\n' "$element" | awk '{ print toupper($1) }')
      case "$word" in
        WORKDIR)
          workdirs=$((workdirs + 1))
          if [ "$workdirs" -gt 1 ] || [ "$check_seen" = 1 ]; then
            checked=$((checked + 1))
            refuse "the Dockerfile xray-builder stage sets '$shown' after its first WORKDIR or its golang.org/x/net go.mod check, so the go.mod check need not read the go.mod the build uses"
          fi
          ;;
        SHELL)
          checked=$((checked + 1))
          refuse "the Dockerfile xray-builder stage sets '$shown', so the go.mod check need not read the go.mod the build uses"
          ;;
        COPY | ADD)
          if [[ $scan =~ $gomod_name_re ]] || ! [[ $scan =~ $patch_copy_re ]]; then
            checked=$((checked + 1))
            refuse "the Dockerfile xray-builder stage runs '$shown', which writes go.mod or can, since it copies outside /patches/"
          fi
          ;;
      esac
    fi
    if [[ $scan =~ $gomod_tool_re ]] || [[ $bare =~ $gomod_tool_re ]]; then
      checked=$((checked + 1))
      refuse "the Dockerfile xray-builder stage runs '$shown', which can change the golang.org/x/net requirement in go.mod outside its go.mod check"
    fi
    if [[ $scan =~ $chdir_re ]] || [[ $bare =~ $chdir_re ]]; then
      checked=$((checked + 1))
      refuse "the Dockerfile xray-builder stage runs '$shown', so the go.mod check need not read the go.mod the build uses"
    fi
    if [[ $scan =~ $gomod_write_re ]] || [[ $scan =~ $redirect_re ]] || { [[ $scan =~ $foreign_apply_re ]] && ! [[ $scan =~ $apply_re ]]; }; then
      checked=$((checked + 1))
      refuse "the Dockerfile xray-builder stage runs '$shown', which writes go.mod"
    fi
    if [ "$check_seen" = 1 ] && [[ $scan =~ $git_move_re ]]; then
      checked=$((checked + 1))
      refuse "the Dockerfile xray-builder stage runs '$shown' after its golang.org/x/net go.mod check, which can replace the checked go.mod"
    fi
    if [ "$safety" != inst ]; then
      if [ "$safety" = exec ]; then
        if [[ $element == *\\* ]]; then
          commands=$'\\\037\037\037'
        else
          commands=$(printf '%s\n' "$scan" | awk '{ rest = ""; for (i = 3; i <= NF; i++) rest = rest (rest == "" ? "" : " ") $i; printf "%s\037%s\037%s\037\n", $1, $2, rest }')
        fi
      else
        commands=$(printf '%s\n' "$element" | command_words)
      fi
      while IFS=$'\037' read -r name sub rest assigns; do
        [ -n "$name" ] || continue
        if [[ $name == *[\$\`]* ]] || { { [[ $name =~ $export_names_re ]] || { [ "$name" = go ] && [ "$sub" = env ]; }; } && [[ "$sub $rest" == *[\$\`]* ]]; }; then
          checked=$((checked + 1))
          refuse "the Dockerfile xray-builder stage runs '$shown', which has a \$ or a backquote in a command name or in what it exports, so the command it runs cannot be read"
        fi
        if [ "$check_seen" = 1 ] && ! allowed_after_check "$name" "$sub" "$rest" "$assigns"; then
          checked=$((checked + 1))
          refuse "the Dockerfile xray-builder stage runs '$shown', whose command '${name:0:40}' is not grep, test, git apply of a /patches/ patch or go build, after its golang.org/x/net go.mod check"
        fi
      done <<EOF
$commands
EOF
    fi
    case "$safety" in inst | exec) continue ;; esac
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
    check_seen=1
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

stage=preamble
froms_total=$(printf '%s\n' "$dockerfile_chain" | awk -F '\t' '$1 == "inst" && $2 ~ /^[Ff][Rr][Oo][Mm][[:space:]]/ { n++ } END { print n + 0 }')
froms_seen=0
while IFS=$'\t' read -r safety element; do
  [ -n "$element" ] || continue
  if [ "$safety" = inst ] && [[ $element =~ ^[Ff][Rr][Oo][Mm][[:space:]] ]]; then
    stage=$(printf '%s\n' "$element" | awk 'NF >= 3 && tolower($(NF - 1)) == "as" { print tolower($NF); next } { print "unnamed" }')
    froms_seen=$((froms_seen + 1))
    continue
  fi
  if [ "$froms_seen" -gt 0 ] && [ "$froms_seen" = "$froms_total" ] && [ "$safety" != inst ] && ! final_stage_run_allowed "$safety" "$element"; then
    checked=$((checked + 1))
    refuse "the Dockerfile final stage runs '${element:0:120}', which is not apk update, apk add of package names, command -v or test, so it can write /usr/local/bin/xray after the COPY from xray-builder"
  fi
  [ "$stage" != xray-builder ] || continue
  element_forms "$safety" "$element"
  check_toolchain_env "$stage" "$element"
done <<EOF
$dockerfile_chain
EOF

xray_copies=0
while IFS=$'\t' read -r kind source dest; do
  [ -n "$kind" ] || continue
  case "$kind" in
    add)
      checked=$((checked + 1))
      refuse "the Dockerfile ADDs /usr/local/bin/xray from '$source'; only COPY --from=xray-builder may write it"
      ;;
    dir)
      checked=$((checked + 1))
      refuse "the Dockerfile copies from '$source' into the directory $dest, so it can create /usr/local/bin/xray without the xray-builder stage"
      ;;
    copy)
      if [ "$source" = xray-builder ]; then
        xray_copies=$((xray_copies + 1))
      else
        checked=$((checked + 1))
        refuse "the Dockerfile copies /usr/local/bin/xray from '$source', not from the xray-builder stage"
      fi
      ;;
  esac
done <<EOF
$(printf '%s\n' "$dockerfile_chain" | awk -F '\t' '
  $1 == "inst" {
    line = substr($0, index($0, "\t") + 1)
    split(line, head, /[[:space:]]+/)
    kind = toupper(head[1])
    if (kind != "COPY" && kind != "ADD") next
    gsub(/[][",]/, " ", line)
    n = split(line, part, /[[:space:]]+/)
    while (n > 0 && part[n] == "") n--
    dest = part[n]
    gsub(/\/+/, "/", dest)
    while (sub(/\/\.\//, "/", dest)) {}
    sub(/\/\.$/, "", dest)
    dir = dest
    sub(/\/$/, "", dir)
    from = "the build context"
    for (i = 2; i < n; i++) if (tolower(part[i]) ~ /^--from=/) from = tolower(substr(part[i], 8))
    if (dest == "/usr/local/bin/xray") printf "%s\t%s\t%s\n", (kind == "ADD" ? "add" : "copy"), from, dest
    else if (dir == "" || dir == "/usr" || dir == "/usr/local" || dir == "/usr/local/bin") printf "dir\t%s\t%s\n", from, dest
  }
')
EOF
if [ -f Dockerfile ] && [ "$xray_copies" = 0 ]; then
  checked=$((checked + 1))
  refuse "the Dockerfile has no COPY --from=xray-builder of /usr/local/bin/xray, so the image's Xray binary is not the one the xray-builder stage checks"
fi

make_commit_line="XRAY_COMMIT ?= \$(shell sed -n 's/^ARG XRAY_COMMIT=//p' Dockerfile)"
make_builds_docker_commit=0
if [ -n "$upstream_xnet" ] && [ -f Makefile ] \
  && [ "$(grep -cE '^[[:space:]]*(export[[:space:]]+|override[[:space:]]+)*XRAY_COMMIT[[:space:]]*(\?|:|::|:::|\+|!)?=' Makefile || true)" = 1 ] \
  && grep -qxF "$make_commit_line" Makefile \
  && [ "$(grep -c '^ARG XRAY_COMMIT=' Dockerfile || true)" = 1 ] \
  && grep -q '^ARG XRAY_COMMIT=' <<<"$xray_stage"; then
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
