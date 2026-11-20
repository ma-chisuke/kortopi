#!/usr/bin/env bash
# Build, check, publish, and verify a release of kortopi (ADR-0030,
# docs/guides/release.md). The make targets in make/release.mk call it, and
# .github/workflows/release.yml calls those targets.
#
# Usage: release.sh build <version> [<directory>]    make release-build
#        release.sh gate <version>                   make release-gate
#        release.sh publish <version> [<directory>]  make release-publish
#        release.sh verify <version>                 make release-verify
#        release.sh test [<directory>]               make test-release
#
#   build    Build a static binary of the commit that is checked out for each
#            platform, and pack each with LICENSE, the README, and the notices
#            of the third-party software into <directory> (default dist/):
#              kortopi_<version>_<os>_<arch>.tar.gz   (.zip for windows)
#              kortopi_<version>_checksums.txt        SHA-256 of each archive
#            The same commit and the same Go give the same bytes.
#   gate     Check that the tag <version> is checked out, that it is on main,
#            and that the check `check` of CI passed on its commit.
#   publish  Check the checksums and the provenance of the files in
#            <directory>, then create the GitHub release <version> with them
#            and generated notes.
#   verify   Download the published release <version>, and check its
#            checksums, its provenance, and the attestation of the release.
#   test     Build twice with a test version into <directory> (default
#            .cache/release-test), and check that the two builds are the same,
#            that the binary of this machine prints the version, and that the
#            notices list exactly the modules of the binaries.
#
# gate, publish, and verify use gh, logged in, or GH_TOKEN in CI. The
# repository is GH_REPO, or the one of the current directory.
set -euo pipefail

# The platforms of a release.
platforms=(linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64 windows/arm64)
# A version is a semantic version with a leading v, as a Git tag and a Go
# module version are.
version_re='^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*)?$'
# The workflow that builds and attests a release, and the app and the name of
# the check of CI that must pass first (.github/workflows/ci.yml).
workflow=.github/workflows/release.yml
actions_app_id=15368
ci_check=check

command=${1:-}
name="release-$command"

usage() {
  sed -n '6,10p' "$0" | sed 's/^# \{0,1\}//' >&2
  exit 2
}

fail() {
  echo "$name: $*" >&2
  exit 1
}

check_version() {
  if ! [[ $1 =~ $version_re ]]; then
    echo "$name: VERSION '$1' is not a version such as v1.2.3 or v1.2.3-rc.1" >&2
    exit 2
  fi
}

# The major version must be one that the module path takes, or go install and
# the module version in the binary cannot use the tag: v0 and v1 without a
# suffix /vN, and vN with it. Run in the root of the repository.
check_major() {
  local version=$1 module major suffix
  module=$(sed -n 's/^module //p' go.mod)
  major=${version#v}
  major=${major%%.*}
  suffix=${module##*/}
  if [[ $suffix =~ ^v[0-9]+$ ]] && [ "$module" != "${module%/*}" ]; then
    [ "v$major" = "$suffix" ] || fail "VERSION '$version' does not match the module path $module, which takes $suffix only"
  elif [ "$major" -gt 1 ]; then
    fail "VERSION '$version' is a major version $major, and the module path $module takes v0 and v1 only. Fix: a v$major moves the module to $module/v$major first"
  fi
}

repository() {
  if [ -n "${GH_REPO:-}" ]; then
    printf '%s\n' "$GH_REPO"
  else
    gh repo view --json nameWithOwner --jq .nameWithOwner
  fi
}

# The archive of a platform, without its extension.
archive_name() {
  local version=$1 os=$2 arch=$3
  printf 'kortopi_%s_%s_%s\n' "$version" "$os" "$arch"
}

archive_ext() {
  if [ "$1" = windows ]; then echo zip; else echo tar.gz; fi
}

checksum_file() {
  printf 'kortopi_%s_checksums.txt\n' "$1"
}

# Check the files listed in a checksum file, in the directory of that file.
sha256_check() {
  local dir=$1 file=$2
  if command -v sha256sum > /dev/null; then
    (cd "$dir" && sha256sum --check --quiet "$file")
  else
    (cd "$dir" && shasum --algorithm 256 --check --quiet "$file")
  fi
}

cmd_build() {
  local version=$1 dist=${2:-dist}
  check_version "$version"
  local root
  root=$(git rev-parse --show-toplevel)
  cd "$root"
  check_major "$version"
  case "$dist" in
    /*) ;;
    *) dist="$root/$dist" ;;
  esac

  if [ -n "$(git status --porcelain --untracked-files=no)" ]; then
    echo "$name: warning: the working tree has changes, so the binaries record vcs.modified=true and match no commit" >&2
  fi
  # Every time in the archives is the time of the commit.
  local epoch=${SOURCE_DATE_EPOCH:-$(git log -1 --format=%ct)}

  rm -rf "$dist"
  local work="$dist/.work"
  mkdir -p "$work"
  (cd tools && go build -o "$work/release" ./cmd/release)

  # Only what the commit holds decides the bytes: no cgo, no paths of this
  # machine (-trimpath), and the settings of the environment that the go
  # command records pinned to their defaults.
  local p os arch exe
  for p in "${platforms[@]}"; do
    os=${p%/*}
    arch=${p#*/}
    exe=kortopi
    if [ "$os" = windows ]; then exe=kortopi.exe; fi
    CGO_ENABLED=0 GOOS=$os GOARCH=$arch GOAMD64=v1 GOARM64=v8.0 GOEXPERIMENT='' GOFLAGS=-mod=readonly \
      go build -trimpath -ldflags="-X main.version=$version" -o "$work/$os-$arch/$exe" ./cmd/kortopi
  done

  # The licenses of the modules, as Trivy identifies them under trivy.yaml,
  # which marks a forbidden license CRITICAL.
  TRIVY_DISABLE_TELEMETRY=true TRIVY_SKIP_VERSION_CHECK=true \
    trivy fs --quiet --scanners license --severity UNKNOWN,LOW,MEDIUM,HIGH,CRITICAL \
    --format json --output "$work/licenses.json" go.mod
  "$work/release" notices -root "$root" -version "$version" -trivy "$work/licenses.json" \
    -o "$work/THIRD_PARTY_NOTICES.txt" "$work"/*-*/kortopi*

  local archive archives=()
  for p in "${platforms[@]}"; do
    os=${p%/*}
    arch=${p#*/}
    exe=kortopi
    if [ "$os" = windows ]; then exe=kortopi.exe; fi
    archive="$(archive_name "$version" "$os" "$arch")"
    "$work/release" archive -o "$dist/$archive.$(archive_ext "$os")" -dir "$archive" -mtime "$epoch" \
      "$exe=$work/$os-$arch/$exe" LICENSE=LICENSE README.md=README.md README.ja.md=README.ja.md \
      "THIRD_PARTY_NOTICES.txt=$work/THIRD_PARTY_NOTICES.txt"
    archives+=("$dist/$archive.$(archive_ext "$os")")
  done
  "$work/release" checksums -o "$dist/$(checksum_file "$version")" "${archives[@]}"
  rm -rf "$work"
  echo "$name: built kortopi $version for ${#platforms[@]} platforms into $dist:"
  (cd "$dist" && cat "$(checksum_file "$version")")
}

cmd_gate() {
  local version=$1
  check_version "$version"
  cd "$(git rev-parse --show-toplevel)"
  check_major "$version"
  local commit head repo
  commit=$(git rev-parse --verify --quiet "refs/tags/$version^{commit}") || fail "the tag $version does not exist here"
  head=$(git rev-parse HEAD)
  if [ "$commit" != "$head" ]; then
    fail "the tag $version points to $commit, but $head is checked out"
  fi
  git rev-parse --verify --quiet refs/remotes/origin/main > /dev/null || fail "origin/main is missing; fetch it first"
  if ! git merge-base --is-ancestor "$commit" refs/remotes/origin/main; then
    fail "the tag $version points to $commit, which is not on main. Fix: delete the tag, and tag a commit of main"
  fi

  repo=$(repository)
  local runs total passed
  runs=$(gh api "repos/$repo/commits/$commit/check-runs?check_name=$ci_check&app_id=$actions_app_id&filter=latest" \
    --jq '[.check_runs[] | {status, conclusion}]')
  total=$(jq length <<< "$runs")
  passed=$(jq '[.[] | select(.status == "completed" and .conclusion == "success")] | length' <<< "$runs")
  if [ "$total" -eq 0 ]; then
    fail "CI has not reported the check '$ci_check' on $commit. Fix: wait for CI on main, then run the release workflow again"
  fi
  if [ "$passed" -ne "$total" ]; then
    fail "the check '$ci_check' of CI on $commit has not passed: $(jq -c . <<< "$runs"). Fix: wait for CI or fix main, then run the release workflow again"
  fi
  echo "$name: $version is $commit, on main, and CI passed on it"
}

# Check the checksums and the provenance of the files of a release in a
# directory.
verify_files() {
  local version=$1 dir=$2 repo=$3
  local sums
  sums=$(checksum_file "$version")
  [ -f "$dir/$sums" ] || fail "$dir has no $sums; run make release-build VERSION=$version"
  sha256_check "$dir" "$sums" || fail "the files in $dir do not match $sums"

  local p os arch file files=()
  for p in "${platforms[@]}"; do
    os=${p%/*}
    arch=${p#*/}
    file="$(archive_name "$version" "$os" "$arch").$(archive_ext "$os")"
    grep -q "  $file\$" "$dir/$sums" || fail "$sums does not list $file"
    files+=("$dir/$file")
  done
  files+=("$dir/$sums")
  # Built by the release workflow of this repository, from the tag.
  for file in "${files[@]}"; do
    gh attestation verify "$file" --repo "$repo" --signer-workflow "$repo/$workflow" \
      --source-ref "refs/tags/$version" > /dev/null || fail "no valid provenance for $(basename "$file")"
  done
  echo "$name: the checksums and the provenance of ${#files[@]} files match"
}

cmd_publish() {
  local version=$1 dir=${2:-dist}
  check_version "$version"
  local repo
  repo=$(repository)
  verify_files "$version" "$dir" "$repo"

  # A draft is what a run that failed while it uploaded leaves behind; a
  # published release is final.
  local draft
  draft=$(gh release view "$version" --repo "$repo" --json isDraft --jq .isDraft 2> /dev/null || true)
  case "$draft" in
    true)
      echo "$name: deleting the draft of $version that an earlier run left"
      gh release delete "$version" --repo "$repo" --yes
      ;;
    false) fail "the release $version is already published" ;;
  esac

  local flags=(--repo "$repo" --verify-tag --title "$version" --generate-notes)
  if [[ $version == *-* ]]; then
    flags+=(--prerelease)
  fi
  local files=()
  local f
  for f in "$dir"/*; do
    files+=("$f")
  done
  gh release create "$version" "${flags[@]}" "${files[@]}"
}

cmd_verify() {
  local version=$1
  check_version "$version"
  local repo tmp
  repo=$(repository)
  tmp=$(mktemp -d)
  trap 'rm -rf "$tmp"' EXIT
  gh release download "$version" --repo "$repo" --dir "$tmp"
  verify_files "$version" "$tmp" "$repo"
  # The release attestation that GitHub writes for an immutable release.
  gh release verify "$version" --repo "$repo" > /dev/null || fail "the release $version has no valid attestation; is it immutable?"
  local f
  for f in "$tmp"/*; do
    gh release verify-asset "$version" "$f" --repo "$repo" > /dev/null || fail "$(basename "$f") is not an asset of the release $version"
  done
  echo "$name: the release $version and its assets are verified"
}

cmd_test() {
  local out=${1:-.cache/release-test} version=v0.0.0-test
  local root
  root=$(git rev-parse --show-toplevel)
  cd "$root"
  case "$out" in
    /*) ;;
    *) out="$root/$out" ;;
  esac
  rm -rf "$out"
  local sums
  sums=$(checksum_file "$version")
  name=release-build
  cmd_build "$version" "$out/first"
  cmd_build "$version" "$out/second"
  name=test-release
  if ! cmp -s "$out/first/$sums" "$out/second/$sums"; then
    diff "$out/first/$sums" "$out/second/$sums" | sed -n \
      "s|^> [0-9a-f]*  \(.*\)|$out/second/$sums:1: [release/not-reproducible] \1 differs between two builds of one commit|p"
    exit 1
  fi

  # The binary of this machine, if it is a platform of a release.
  local os arch archive
  os=$(go env GOHOSTOS)
  arch=$(go env GOHOSTARCH)
  archive="$(archive_name "$version" "$os" "$arch")"
  if [ -f "$out/first/$archive.tar.gz" ]; then
    mkdir -p "$out/unpacked"
    tar -xzf "$out/first/$archive.tar.gz" -C "$out/unpacked"
    local printed
    printed=$("$out/unpacked/$archive/kortopi" version)
    if [ "$printed" != "kortopi $version" ]; then
      echo "$out/first/$archive.tar.gz:1: [release/version] kortopi version printed '$printed', not 'kortopi $version'"
      exit 1
    fi
    # The list at the top of the notices names exactly the modules that the
    # binary holds, as `go version -m` lists them.
    local notices="$out/unpacked/$archive/THIRD_PARTY_NOTICES.txt" want got
    want=$(go version -m "$out/unpacked/$archive/kortopi" | awk '$1 == "dep" { print $2 " " $3 }')
    got=$(awk '/^=+$/ { exit } /^- / && !/^- Go / { sub(/^- /, ""); sub(/ \(.*\)$/, ""); print }' "$notices")
    if [ "$want" != "$got" ]; then
      diff <(printf '%s\n' "$want") <(printf '%s\n' "$got") | sed -n \
        -e "s|^< \(.*\)|$notices:1: [release/notices] \1 is in the binary and not in the notices|p" \
        -e "s|^> \(.*\)|$notices:1: [release/notices] \1 is in the notices and not in the binary|p"
      exit 1
    fi
  fi
  rm -rf "$out"
}

case "$command" in
  build)
    [ "$#" -ge 2 ] && [ "$#" -le 3 ] || usage
    cmd_build "${@:2}"
    ;;
  gate)
    [ "$#" -eq 2 ] || usage
    cmd_gate "$2"
    ;;
  publish)
    [ "$#" -ge 2 ] && [ "$#" -le 3 ] || usage
    cmd_publish "${@:2}"
    ;;
  verify)
    [ "$#" -eq 2 ] || usage
    cmd_verify "$2"
    ;;
  test)
    [ "$#" -le 2 ] || usage
    cmd_test "${@:2}"
    ;;
  *) usage ;;
esac
