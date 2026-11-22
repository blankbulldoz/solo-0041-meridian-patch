#!/usr/bin/env bash
# 零距离判定迁移手册的配套脚本，与 docs/zero-distance-migration.md 中的
# 同一组请求逐条对应：S1–S5 种子、O1 旧路径对照、Q1–Q9 四种输入的判定、
# A1–A2 存量布尔消费者的旧数据处理与条件写演示。
#
# 用法：
#   1. 用空数据目录启动服务：  ./meridian -listen 127.0.0.1:8741 -data ./data
#   2. 运行本脚本：            examples/zero-distance-migration.sh
#   非默认地址：               BASE=http://127.0.0.1:9000 examples/zero-distance-migration.sh
#
# 安装 jq 时脚本会断言每个响应的关键字段（含“同一修订”校验）；
# 没有 jq 时只打印请求与响应，退出码仍为 0。
set -euo pipefail

BASE="${BASE:-http://127.0.0.1:8741}/meridian/v1"

JQ=0
if command -v jq >/dev/null 2>&1; then JQ=1; fi
failures=0

BODY=""
CODE=""

req() { # 标签 方法 路径 [请求体]
  local label="$1" method="$2" path="$3" body="${4:-}"
  echo
  echo "### $label"
  echo "$method $path"
  if [ -n "$body" ]; then echo "$body"; fi
  local args=(-sS -X "$method" -w '\n%{http_code}')
  if [ -n "$body" ]; then
    args+=(-H 'Content-Type: application/json' -d "$body")
  fi
  local raw
  raw="$(curl "${args[@]}" "$BASE$path")"
  CODE="${raw##*$'\n'}"
  BODY="${raw%$'\n'*}"
  if [ "$JQ" -eq 1 ]; then
    printf '%s\n' "$BODY" | jq .
  else
    printf '%s\n' "$BODY"
  fi
  echo "HTTP $CODE"
}

assert() { # jq 表达式（作用于上一条响应体）
  if [ "$JQ" -eq 0 ]; then return 0; fi
  if printf '%s' "$BODY" | jq -e "$1" >/dev/null; then
    echo "ok: $1"
  else
    echo "ASSERT FAILED: $1" >&2
    failures=$((failures + 1))
  fi
}

assert_code() { # 期望的 HTTP 状态码
  if [ "$CODE" = "$1" ]; then
    echo "ok: HTTP $1"
  else
    echo "ASSERT FAILED: HTTP $CODE, want $1" >&2
    failures=$((failures + 1))
  fi
}

# 前置：服务可达且种子图层尚不存在（保证 S1–S4 之后全库修订恰好是 4）。
probe="$(curl -sS "$BASE/strata")"
case "$probe" in
  *'"name":"plots"'*|*'"name":"overlays"'*|*'"name":"markers"'*)
    echo "种子图层已存在：请用空数据目录重启服务后再运行本脚本" >&2
    exit 1
    ;;
esac

BOUNDS='{"min_x":-20,"min_y":-20,"max_x":24,"max_y":24}'

req S1 POST /strata "{\"name\":\"plots\",\"kind\":\"extent\",\"bounds\":$BOUNDS,\"exclusive\":false}"
assert_code 201
assert '.revision == 1'

req S2 POST /strata "{\"name\":\"overlays\",\"kind\":\"extent\",\"bounds\":$BOUNDS,\"exclusive\":false}"
assert_code 201
assert '.revision == 2'

req S3 POST /strata "{\"name\":\"markers\",\"kind\":\"mark\",\"bounds\":$BOUNDS,\"exclusive\":false}"
assert_code 201
assert '.revision == 3'

req S4 POST /edits '{
  "edits": [
    {"layer":"plots","id":"alpha","mode":"insert","value":{"id":"alpha","class":"parcel","shape":{"extent":{"min_x":0,"min_y":0,"max_x":4,"max_y":3}}}},
    {"layer":"plots","id":"beta","mode":"insert","value":{"id":"beta","class":"parcel","shape":{"extent":{"min_x":4,"min_y":0,"max_x":8,"max_y":2}}}},
    {"layer":"plots","id":"gamma","mode":"insert","value":{"id":"gamma","class":"parcel","shape":{"extent":{"min_x":4,"min_y":3,"max_x":8,"max_y":6}}}},
    {"layer":"plots","id":"delta","mode":"insert","value":{"id":"delta","class":"parcel","shape":{"extent":{"min_x":10,"min_y":0,"max_x":16,"max_y":6}}}},
    {"layer":"overlays","id":"epsilon","mode":"insert","value":{"id":"epsilon","class":"overlay","shape":{"extent":{"min_x":14,"min_y":4,"max_x":20,"max_y":10}}}},
    {"layer":"markers","id":"pin-edge","mode":"insert","value":{"id":"pin-edge","class":"survey","shape":{"mark":{"x":4,"y":1}}}},
    {"layer":"markers","id":"pin-corner","mode":"insert","value":{"id":"pin-corner","class":"survey","shape":{"mark":{"x":4,"y":3}}}}
  ]
}'
assert_code 200
assert '.revision == 4'

req S5 GET /status
assert '.revision == 4 and .writable == true'

# 旧路径对照：把 distance_squared == 0 的邻居全部记为碰撞。
req O1 POST /strata/plots/nearest '{"point":{"x":4,"y":1},"limit":4}'
assert '.revision == 4'
assert '([.neighbors[] | select(.distance_squared == 0) | .feature.id]) == ["alpha","beta"]'

# 输入 1：半开边上的点。pin-edge (4,1) 在 alpha 的右上（排除）边上。
req Q1 POST /relation '{"left":{"layer":"plots","id":"alpha"},"right":{"layer":"markers","id":"pin-edge"}}'
assert '.revision == 4 and .left_revision == 4 and .right_revision == 4'
assert '.intersects == false and .distance_squared == 0'

req Q2 POST /strata/plots/locate '{"x":4,"y":1}'
assert '.revision == 4 and .layer_revision == 4'
assert '[.features[].id] == ["beta"]'

# 输入 2：角触。alpha 与 gamma 只在角点 (4,3) 接触。
req Q3 POST /relation '{"left":{"layer":"plots","id":"alpha"},"right":{"layer":"plots","id":"gamma"}}'
assert '.revision == 4 and .left_revision == 4 and .right_revision == 4'
assert '.intersects == false and .distance_squared == 0'
assert 'has("intersection") | not'

req Q4 POST /relation '{"left":{"layer":"plots","id":"gamma"},"right":{"layer":"markers","id":"pin-corner"}}'
assert '.revision == 4'
assert '.intersects == true and .left_contains == true and .right_contains == false'

# 全窗共享边：角触不产生共享边；同时作为输入 4 的未裁切基线。
req Q5 POST /strata/plots/seams '{"bounds":{"min_x":0,"min_y":0,"max_x":8,"max_y":6},"limit":10}'
assert '.revision == 4 and .layer_revision == 4'
assert '.seams == [{"first":"alpha","second":"beta","seam":{"axis":"vertical","fixed":4,"from":0,"to":2}}]'

# 输入 3：裁切前相交而窗内不相交。delta 与 epsilon 的正面积交集在窄窗之外。
req Q6 POST /relation '{"left":{"layer":"plots","id":"delta"},"right":{"layer":"overlays","id":"epsilon"}}'
assert '.revision == 4 and .left_revision == 4 and .right_revision == 4'
assert '.intersects == true and .distance_squared == 0'
assert '.intersection == {"min_x":14,"min_y":4,"max_x":16,"max_y":6}'

req Q7 POST /join '{"left":"plots","right":"overlays","bounds":{"min_x":10,"min_y":0,"max_x":14,"max_y":4},"limit":10}'
assert '.revision == 4 and .left_revision == 4 and .right_revision == 4'
assert '.pairs == []'

req Q8 POST /join '{"left":"plots","right":"overlays","bounds":{"min_x":10,"min_y":0,"max_x":20,"max_y":10},"limit":10}'
assert '.revision == 4 and .left_revision == 4 and .right_revision == 4'
assert '.pairs == [{"left":"delta","right":"epsilon","intersection":{"min_x":14,"min_y":4,"max_x":16,"max_y":6}}]'

# 输入 4：共享边被窗裁成较短线。与 Q5 同一对、同一修订，from/to 被窗口裁短。
req Q9 POST /strata/plots/seams '{"bounds":{"min_x":3,"min_y":-1,"max_x":5,"max_y":1},"limit":10}'
assert '.revision == 4 and .layer_revision == 4'
assert '.seams == [{"first":"alpha","second":"beta","seam":{"axis":"vertical","fixed":4,"from":0,"to":1}}]'

# 路径 A 演示：旧数据行引用的地物已删除时，/relation 返回 404，不得用旧布尔猜测。
req A1 POST /relation '{"left":{"layer":"plots","id":"beta"},"right":{"layer":"markers","id":"pin-gone"}}'
assert_code 404
assert '.code == "missing"'

# 路径 A 演示：基于同一修订的条件写。陈旧的 if_revision 被 409 拒绝且不推进修订。
req A2 POST /edits '{"if_revision":999,"edits":[{"layer":"plots","id":"alpha","mode":"remove"}]}'
assert_code 409
assert '.code == "conflict"'

req A3 GET /status
assert '.revision == 4'

echo
if [ "$JQ" -eq 0 ]; then
  echo "未安装 jq，仅打印响应，未做字段断言。"
elif [ "$failures" -gt 0 ]; then
  echo "$failures 条断言失败" >&2
  exit 1
else
  echo "全部断言通过。"
fi
