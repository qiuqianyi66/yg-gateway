#!/usr/bin/env python3
"""compare-est-models.py —— 主口径 vs 影子口径的真实流量对照。

为什么需要（2026-11-26 16:35）：归档现在同时落 est_prompt_tokens（主口径：
CJK 1 / ASCII 0.38 / 结构 0.10 / 图 1450）和 est_shadow_tokens（影子口径：
总字节/3.4 + 350/图）。两套假设在图片密集的会话里都能拟合到 1.0，只有等
「图片少、文本多」的样本出现，才能看出谁的误差更小。

用法：
  python scripts/compare-est-models.py [--since 2026-11-26T16:33:00] [dir]

输出按上游规模分桶 + 按图片张数分桶，分别给 median/p90；再打印两口径的
逐行差值（shadow/main），差值为负说明影子口径更保守地估计图片成本。
只在样本 >= 5 的桶里给「谁更接近 1.0」的倾向，避免单样本下结论。
"""
import argparse
import glob
import json
import os
import re
import statistics
import sys


def load(log_dir, since):
    rows = []
    for path in sorted(glob.glob(os.path.join(log_dir, "requests-*.jsonl"))):
        with open(path, encoding="utf-8") as fh:
            for line in fh:
                line = line.strip()
                if not line:
                    continue
                try:
                    r = json.loads(line)
                except json.JSONDecodeError:
                    continue
                if not (r.get("est_prompt_tokens") and r.get("prompt_tokens")):
                    continue
                if not r.get("est_shadow_tokens"):
                    # 影子口径 16:33 才落档；更早的行用同一公式从构成反算，
                    # 这样历史样本也能参与对照（公式是确定性的，不引入新假设）。
                    if r.get("est_struct_bytes") is None:
                        continue
                    b = 3 * (r.get("est_wide_chars") or 0) + (r.get("est_other_chars") or 0) + r["est_struct_bytes"]
                    r["est_shadow_tokens"] = (b * 10 + 33) // 34 + 350 * (r.get("est_images") or 0)
                if since and (r.get("time") or "") < since:
                    continue
                rows.append(r)
    return rows


def bucket_of(r):
    up = r["prompt_tokens"]
    if up < 10000:
        return "up<10k"
    if up < 50000:
        return "up 10-50k"
    if up < 200000:
        return "up 50-200k"
    return "up 200k+"


def img_bucket(r):
    n = r.get("est_images") or 0
    if n == 0:
        return "img 0"
    if n <= 5:
        return "img 1-5"
    if n < 20:
        return "img 6-19"
    return "img 20+"


def stats(vals):
    if not vals:
        return None
    vals = sorted(vals)
    p90 = vals[int(len(vals) * 0.9)] if len(vals) > 1 else vals[0]
    return {
        "n": len(vals),
        "median": statistics.median(vals),
        "p90": p90,
        "min": vals[0],
        "max": vals[-1],
    }


def report(title, groups, key):
    print(f"\n{title}")
    for name, rows in sorted(groups.items()):
        main = stats([r["est_prompt_tokens"] / r["prompt_tokens"] for r in rows])
        shadow = stats([r["est_shadow_tokens"] / r["prompt_tokens"] for r in rows])
        if not main:
            continue
        closer = "-"
        if main["n"] >= 5:
            d_main = abs(main["median"] - 1.0)
            d_shadow = abs(shadow["median"] - 1.0)
            closer = "main" if d_main <= d_shadow else "shadow"
        print("  %-11s n=%-3d main median=%.2f p90=%.2f | shadow median=%.2f p90=%.2f | closer=%s"
              % (name, main["n"], main["median"], main["p90"],
                 shadow["median"], shadow["p90"], closer))


def main():
    ap = argparse.ArgumentParser(description="compare main vs shadow token estimators")
    ap.add_argument("log_dir", nargs="?", default=r"D:\yg-gateway\data\request-logs")
    ap.add_argument("--since", default="", help="只统计 time >= 该 ISO 时间的行")
    args = ap.parse_args()
    if not os.path.isdir(args.log_dir):
        print(f"no archive dir: {args.log_dir}")
        return 3
    rows = load(args.log_dir, args.since)
    if not rows:
        print("no rows with est_shadow_tokens yet（影子口径 2026-11-26 16:33 才部署）")
        return 2
    by_size, by_img = {}, {}
    for r in rows:
        by_size.setdefault(bucket_of(r), []).append(r)
        by_img.setdefault(img_bucket(r), []).append(r)
    print(f"samples={len(rows)}（since={args.since or '-'}）")
    report("按上游规模", by_size, "up")
    report("按图片张数", by_img, "img")
    print("\n逐行差值 shadow-main（前 10 条，负值=影子更便宜）：")
    for r in rows[-10:]:
        d = r["est_shadow_tokens"] - r["est_prompt_tokens"]
        print("  %s img=%-2s up=%-7d main=%-7d shadow=%-7d delta=%+d"
              % (r.get("time", "")[11:19], r.get("est_images") or 0,
                 r["prompt_tokens"], r["est_prompt_tokens"], r["est_shadow_tokens"], d))
    return 0


if __name__ == "__main__":
    sys.exit(main())
