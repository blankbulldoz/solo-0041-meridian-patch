#!/usr/bin/env python3
"""Render SVG shape evidence for every pair (stdlib only, no matplotlib).

For each pair two files are produced:
  evidence figure (figures/evidence/<pair_id>.svg): labeled with the answer-key
  relation flags and class;
  blind figure (figures/blind/<pair_id>.svg): shapes only, no labels, for
  annotators to mark up.
"""
from __future__ import annotations

import argparse
import json
from pathlib import Path

C_TOUCH = "C1_TOUCH_DISJOINT"
C_OVERLAP = "C2_PARTIAL_OVERLAP"
C_REVERSE = "C3_REVERSE_CONTAINMENT"

CLASS_CN = {
    C_TOUCH: "零距离不相交",
    C_OVERLAP: "正面积相交但互不包含",
    C_REVERSE: "反转左右才变成包含",
}


def escape(text):
    return (str(text).replace("&", "&amp;").replace("<", "&lt;")
            .replace(">", "&gt;"))


def render_svg(pair_id, left, right, info, labeled):
    shapes = [left, right]
    pad = 24
    min_x = min(s["min_x"] for s in shapes) - pad
    max_x = max(s["max_x"] for s in shapes) + pad
    min_y = min(s["min_y"] for s in shapes) - pad
    max_y = max(s["max_y"] for s in shapes) + pad
    span_x = max(1, max_x - min_x)
    span_y = max(1, max_y - min_y)
    # keep a uniform scale so axis units are comparable; fit into 640x480
    scale = min(640.0 / span_x, 480.0 / span_y)
    width = span_x * scale
    height = span_y * scale

    def px(x):
        return (x - min_x) * scale

    def py(y):
        return height - (y - min_y) * scale  # SVG y grows downward

    def rect(s, fill, opacity):
        x = px(s["min_x"])
        y = py(s["max_y"])
        w = (s["max_x"] - s["min_x"]) * scale
        h = (s["max_y"] - s["min_y"]) * scale
        return (f'<rect x="{x:.2f}" y="{y:.2f}" width="{w:.2f}" '
                f'height="{h:.2f}" fill="{fill}" fill-opacity="{opacity}" '
                f'stroke="{fill}" stroke-width="2"/>')

    parts = [
        f'<svg xmlns="http://www.w3.org/2000/svg" width="{width:.0f}" '
        f'height="{height + (70 if labeled else 24):.0f}" '
        'font-family="Helvetica, Arial, sans-serif">',
        '<rect width="100%" height="100%" fill="white"/>']
    parts.append(rect(right, "#1f77b4", 0.25))   # right = blue
    parts.append(rect(left, "#d62728", 0.30))    # left = red
    parts.append(f'<text x="8" y="18" font-size="14" fill="#333">'
                 f'{escape(pair_id)}'
                 + (f' · {escape(info["target_class"])} '
                    f'{escape(CLASS_CN[info["target_class"]])}'
                    if labeled else "") + "</text>")
    if labeled:
        flags = (f'intersects={info["intersects"]}  '
                 f'left_contains={info["left_contains"]}  '
                 f'right_contains={info["right_contains"]}  '
                 f'distance_squared={info["distance_squared"]}')
        parts.append(f'<text x="8" y="{height + 40:.0f}" font-size="13" '
                     f'fill="#222">{escape(flags)}</text>')
        parts.append(f'<text x="8" y="{height + 60:.0f}" font-size="13" '
                     f'fill="#444">{escape(info["ordered_relation"])}</text>')
    parts.append("</svg>")
    return "\n".join(parts)


def main():
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument("--root", default=str(Path(__file__).resolve().parents[1]))
    args = ap.parse_args()
    root = Path(args.root)

    blind = json.loads((root / "data" / "blind_input.json").read_text())
    key = json.loads((root / "data" / "answer_key.json").read_text())
    key_by_row = {r["row_id"]: r for r in key["rows"]}

    ev_dir = root / "figures" / "evidence"
    bl_dir = root / "figures" / "blind"
    ev_dir.mkdir(parents=True, exist_ok=True)
    bl_dir.mkdir(parents=True, exist_ok=True)

    rendered = 0
    for row in blind["rows"]:
        if row["direction"] != "forward":
            continue  # one figure per unordered pair
        pid = row["pair_id"]
        info = key_by_row[f"{pid}_forward"]
        svg = render_svg(pid, row["left_shape"], row["right_shape"],
                         info, labeled=True)
        (ev_dir / f"{pid}.svg").write_text(svg + "\n")
        svg_blind = render_svg(pid, row["left_shape"], row["right_shape"],
                               info, labeled=False)
        (bl_dir / f"{pid}.svg").write_text(svg_blind + "\n")
        rendered += 1
    print(json.dumps(dict(pairs_rendered=rendered,
                          evidence_dir=str(ev_dir),
                          blind_dir=str(bl_dir)), ensure_ascii=False, indent=2))


if __name__ == "__main__":
    main()
