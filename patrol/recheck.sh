#!/usr/bin/env bash
# Re-check an existing patrol sample pack against a running Meridian Patch
# service.  Usage:
#   recheck.sh http://127.0.0.1:8801 [pack-directory]
# The pack directory defaults to ./sample-pack next to this script.
set -euo pipefail

base_url="${1:?usage: recheck.sh BASE_URL [PACK_DIR]}"
pack_dir="${2:-$(dirname "$0")/sample-pack}"

exec python3 -I "$(dirname "$0")/patrol_pack.py" verify \
  --base-url "$base_url" --pack "$pack_dir"
