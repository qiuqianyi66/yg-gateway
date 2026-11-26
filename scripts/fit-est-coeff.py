#!/usr/bin/env python3
"""fit-est-coeff.py —— 用归档里的「构成 + 上游 usage」拟合估算权重。

为什么需要（2026-11-26 第十一轮）：只调文本密度治不了图多请求的偏差。真实数据里
出现两簇：3 图请求 est/up=0.89、11-13 图请求 0.80，说明单图固定成本 1450 与真实
视觉计费不符。只有把 wide / other / images 三个原语分开，才能同时解出
「中文每字多少 token、ASCII 每字多少、每张图多少」。

模型：upstream ≈ a*wide + b*other + c*images（不设截距，三项皆为 0 时应为 0）。
数据源：requests-*.jsonl 里同时有 est_wide_chars/est_other_chars 与 prompt_tokens 的行。

诚实边界：
  - 只有两个簇（图多/图少）时三参数不可辨识，c 与 b 会互相吸收——脚本会打印 rank；
  - 样本 < 20 或 rank < 3 时只给「方向性提示」，不作为改权重的依据。
"""
import glob
import json
import sys

import numpy as np

MIN_SAMPLES = 20


def load(paths):
    rows = []
    for path in sorted(set(sum((glob.glob(p) for p in paths), []))):
        with open(path, encoding="utf-8") as fh:
            for line in fh:
                line = line.strip()
                if not line:
                    continue
                try:
                    r = json.loads(line)
                except json.JSONDecodeError:
                    continue
                if not r.get("prompt_tokens"):
                    continue
                if r.get("est_wide_chars") is None and not r.get("est_images") and r.get("est_other_chars") is None:
                    continue
                rows.append(r)
    return rows


def main():
    paths = sys.argv[1:] or [r"D:\yg-gateway\data\request-logs\requests-*.jsonl"]
    rows = load(paths)
    if not rows:
        print("no rows with composition yet（部署 est-composition 之后的请求才会带构成）")
        return 2
    X = np.array([[r.get("est_wide_chars", 0), r.get("est_other_chars", 0), r.get("est_images", 0)] for r in rows], dtype=float)
    y = np.array([r["prompt_tokens"] for r in rows], dtype=float)
    rank = int(np.linalg.matrix_rank(X))
    coef, *_ = np.linalg.lstsq(X, y, rcond=None)
    a, b, c = coef
    pred = X @ coef
    ratios = pred / y
    print(f"n={len(rows)}  rank={rank}/3  a(wide)={a:.3f}  b(other)={b:.3f}  c(image)={c:.0f}")
    print(f"fit ratios: median={np.median(ratios):.2f} p10={np.percentile(ratios,10):.2f} p90={np.percentile(ratios,90):.2f}")
    if rank < 3 or len(rows) < MIN_SAMPLES:
        print(f"UNDERDETERMINED: rank={rank}, n={len(rows)}（只作方向提示，不改权重）")
        return 2
    # 现状权重下的偏差，作为对照
    cur = np.array([r["est_wide_chars"] + (r["est_other_chars"] * 380 + 999) // 1000 + r["est_images"] * 1450 for r in rows], dtype=float)
    print(f"current(1/0.38/1450) ratios: median={np.median(cur / y):.2f} p10={np.percentile(cur/y,10):.2f} p90={np.percentile(cur/y,90):.2f}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
