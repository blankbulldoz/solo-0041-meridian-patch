#!/usr/bin/env bash
# Build Meridian Patch, start a scratch service, seed it from
# seed-scenario.json and regenerate the patrol pack into a fresh directory.
# Requires Go 1.22+; set GO_BIN to point at a non-PATH toolchain.
set -euo pipefail

here="$(cd "$(dirname "$0")" && pwd)"
root="$(cd "$here/.." && pwd)"
go_bin="${GO_BIN:-go}"
total="${TOTAL:-9}"
out="${OUT:-$here/sample-pack}"
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

"$go_bin" -C "$root" build -o "$work/meridian" ./cmd/meridian
mkdir "$work/data"
"$work/meridian" -listen 127.0.0.1:0 -data "$work/data" >"$work/log" 2>&1 &
pid=$!
trap 'kill "$pid" 2>/dev/null || true; rm -rf "$work"' EXIT

address="$(python3 -I - "$work/log" <<'PY'
import json, sys, time
deadline = time.time() + 15
while time.time() < deadline:
    try:
        for line in open(sys.argv[1]):
            try:
                record = json.loads(line)
            except json.JSONDecodeError:
                continue
            if 'address' in record:
                print(record['address'])
                raise SystemExit(0)
    except FileNotFoundError:
        pass
    time.sleep(0.1)
raise SystemExit('service did not start')
PY
)"

python3 -I "$here/patrol_pack.py" seed \
  --base-url "http://$address" --scenario "$here/seed-scenario.json" >/dev/null
python3 -I "$here/patrol_pack.py" generate \
  --base-url "http://$address" \
  --candidates "$here/candidate-probes.json" \
  --total "$total" --out "$out"
python3 -I "$here/patrol_pack.py" verify \
  --base-url "http://$address" --pack "$out" >/dev/null
echo "patrol pack regenerated and verified at $out"
