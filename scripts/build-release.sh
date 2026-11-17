#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
version=${1:?usage: build-release.sh vMAJOR.MINOR.PATCH OUTPUT_DIRECTORY}
out_dir=${2:?output directory required}
if [[ ! "$version" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
  printf 'Release version must use vMAJOR.MINOR.PATCH\n' >&2
  exit 1
fi
mkdir -p "$out_dir"
# Fail on a nonempty directory so stale artifacts cannot be uploaded.
if [[ -n $(find "$out_dir" -mindepth 1 -maxdepth 1 -print -quit) ]]; then
  printf 'Release output directory must be empty\n' >&2
  exit 1
fi
out_dir=$(cd "$out_dir" && pwd)
go version
for target_os in darwin freebsd linux netbsd openbsd; do
  for target_arch in amd64 arm64; do
    asset="lambda-deploy-log-compare-$target_os-$target_arch"
    CGO_ENABLED=0 GOOS="$target_os" GOARCH="$target_arch" go build -trimpath \
      -ldflags "-s -w -X main.appVersion=$version" -o "$out_dir/$asset" .
    go version -m "$out_dir/$asset" > "$out_dir/$asset.buildinfo"
    python3 - "$out_dir/$asset.buildinfo" "$target_os" "$target_arch" <<'PY'
import sys
with open(sys.argv[1]) as stream:
    info = stream.read()
assert 'GOOS=' + sys.argv[2] in info
assert 'GOARCH=' + sys.argv[3] in info
PY
    rm "$out_dir/$asset.buildinfo"
    (cd "$out_dir" && shasum -a 256 "$asset" > "$asset.sha256")
  done
done
(cd "$out_dir" && shasum -a 256 -c ./*.sha256)
# Cross-build checks do not establish native runtime behavior. CI performs native smoke tests.
python3 - "$out_dir" <<'PY'
from pathlib import Path
import sys
root = Path(sys.argv[1])
expected = {
    f'lambda-deploy-log-compare-{os}-{arch}{suffix}'
    for os in ('darwin', 'freebsd', 'linux', 'netbsd', 'openbsd')
    for arch in ('amd64', 'arm64')
    for suffix in ('', '.sha256')
}
assert {p.name for p in root.iterdir()} == expected
PY
