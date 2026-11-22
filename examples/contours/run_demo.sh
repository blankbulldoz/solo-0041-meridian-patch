#!/usr/bin/env bash
# Build the service, start it on an ephemeral port with a temporary data
# directory, and regenerate every contour sample under samples/.
#
# Python runs in isolated mode (-I): the repo and cwd are not placed on
# sys.path, so a planted json.py next to the data files cannot be imported.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"

if ! command -v go >/dev/null 2>&1; then
  echo "go toolchain not found on PATH" >&2
  exit 1
fi

export CGO_ENABLED=0
export GOFLAGS="${GOFLAGS:-} -mod=mod"

cd "$ROOT"
go build ./...
python3 -I "$SCRIPT_DIR/run_demo.py"
