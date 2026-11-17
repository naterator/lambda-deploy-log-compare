#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
version=${1:-dev}
go version
unformatted=$(gofmt -l .)
if [[ -n "$unformatted" ]]; then
  printf 'Run gofmt on:\n%s\n' "$unformatted" >&2
  exit 1
fi
go vet ./...
go test -race ./...
check_dir=$(mktemp -d)
trap 'rm -rf "$check_dir"' EXIT
go build -trimpath -ldflags "-X main.appVersion=$version" -o "$check_dir/compare" .
[[ $("$check_dir/compare" version) == "$version" ]]
"$check_dir/compare" compare --a testdata/compare_baseline.json --b testdata/compare_new.json --json > "$check_dir/report.json"
python3 - "$check_dir/report.json" <<'PY'
import json, sys
with open(sys.argv[1]) as stream:
    report = json.load(stream)
assert report['baseline']['invocation_count'] == 2
assert report['new']['invocation_count'] == 2
assert report['regression']['detected'] is True
PY
# A regression must fail only after emitting a valid report.
if "$check_dir/compare" compare --a testdata/compare_baseline.json --b testdata/compare_new.json --json --fail-on-regression > "$check_dir/regression.json"; then
  printf 'Regression smoke test unexpectedly succeeded\n' >&2
  exit 1
fi
python3 - "$check_dir/regression.json" <<'PY'
import json, sys
with open(sys.argv[1]) as stream:
    assert json.load(stream)['regression']['will_fail'] is True
PY
