#!/usr/bin/env bash
# Reproduce the full blind-review dataset end to end.
# 1. build the Meridian server from the baseline Go sources
# 2. generate samples through the live public /relation HTTP query
# 3. independently re-verify every row from raw coordinates
# 4. render labeled evidence figures and label-free blind figures
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
REPO_ROOT="$(cd "${ROOT}/.." && pwd)"
BIN="$(mktemp -d)/meridian"

# Pick up a user-local Go toolchain if the system PATH has none.
if ! command -v go >/dev/null 2>&1 && [ -x "${HOME}/.local/go/bin/go" ]; then
  export PATH="${HOME}/.local/go/bin:${PATH}"
fi

echo ">> building meridian"
( cd "${REPO_ROOT}" && go build -o "${BIN}" ./cmd/meridian )

echo ">> generating samples via public /relation queries"
python3 -I "${ROOT}/scripts/generate_samples.py" --binary "${BIN}" --out "${ROOT}"

echo ">> independent coordinate re-verification"
python3 -I "${ROOT}/scripts/verify_independently.py" --root "${ROOT}"

echo ">> rendering SVG figures"
python3 -I "${ROOT}/scripts/render_figures.py" --root "${ROOT}"

echo ">> done; artifacts under ${ROOT}/data ${ROOT}/evidence ${ROOT}/figures"
