#!/usr/bin/env python3
"""check-est-drift.py —— 护栏估算 vs 上游实际的漂移守卫。

为什么需要（2026-11-26 第五/六轮）：估算器是启发式（CJK 计 1、其余 0.38/字符、图片固定
成本），它的正确性靠「归档行里 est_prompt_tokens 与 prompt_tokens 成对」来持续核对。
/status 的 est_vs_upstream 只保留最近 20 条环形样本，跨天趋势只能从这里看。

判据（预登记，改动前先改这里；2026-11-26 16:20 收紧）：
  样本 >= 20 才裁决；中位比率必须落在 [0.95, 1.05]，p90 不得超过 1.25。
  中位低于 0.95 = 系统性低估（会漏拦，上游真 413 风险）；高于 1.05 = 系统性保守
  （假性 413、压缩提前）；p90 超 1.25 = 尾部偏差过大。

  为什么从 [0.90,1.10]/1.60 收紧：结构字节（0.10 token/字节）计入后，真实流量 72 条
  的 median=1.01、p90=1.03（其中 200k+ 的 71 条 median=1.01、max=1.03），旧带宽只剩
  「什么都测不出来」的余量。新的 ±5% 带仍留有一定噪声余量（p90 实测 1.03），但能更早
  发现权重回归；换模型/换上游计费口径时应先重跑拟合再加宽。

退出码（四态）：
  0 = 在带内        1 = 漂移（需要重拟合）        2 = 样本不足（未知，不裁决）
  3 = 没有归档文件
"""
import argparse
import glob
import json
import os
import re
import statistics
import sys

BAND_MEDIAN = (0.95, 1.05)
BAND_P90 = 1.25
MIN_SAMPLES = 20


def load_pairs(days, since=""):
    """返回 [(model, est, upstream)]，只取两值都已知的行（unknown 不冒充 0）。"""
    pairs = []
    files = []
    for pat in days:
        files.extend(glob.glob(pat))
    for path in sorted(set(files)):
        with open(path, encoding="utf-8") as fh:
            for line in fh:
                line = line.strip()
                if not line:
                    continue
                try:
                    ev = json.loads(line)
                except json.JSONDecodeError:
                    continue
                est = ev.get("est_prompt_tokens") or 0
                up = ev.get("prompt_tokens") or 0
                if est <= 0 or up <= 0:
                    continue
                ts = ev.get("time") or ""
                if since and ts < since:
                    continue
                # model 字段带账号倍率标签（如 "cn:deepseek-v4.1-flash ×0.11"），
                # 直接分组会把同一模型拆成按账号的多组、永远攒不满 20 条；按基名归并。
                name = re.sub(r"\s+×[0-9.]+$", "", ev.get("model") or "-")
                pairs.append((name, est, up))
    return pairs


def summarize(pairs):
    ratios = sorted(est / up for _, est, up in pairs)
    p90 = ratios[int(len(ratios) * 0.9)] if len(ratios) > 1 else ratios[0]
    return {
        "n": len(ratios),
        "median": statistics.median(ratios),
        "min": ratios[0],
        "p90": p90,
        "max": ratios[-1],
    }


def verdict(s):
    if s["median"] < BAND_MEDIAN[0]:
        return "UNDER (系统性低估，漏拦风险)"
    if s["median"] > BAND_MEDIAN[1]:
        return "OVER (系统性保守，假性 413 风险)"
    if s["p90"] > BAND_P90:
        return "TAIL (p90 超带，图多/长文步骤偏差过大)"
    return "OK"


def main():
    ap = argparse.ArgumentParser(description="guardrail estimate drift guard")
    ap.add_argument("log_dir", nargs="?", default=r"D:\yg-gateway\data\request-logs")
    ap.add_argument("--since", default="", help="只统计 time >= 该 ISO 时间的行（换算法后复验用，如 2026-11-26T14:45:28）")
    args = ap.parse_args()
    log_dir = args.log_dir
    if not os.path.isdir(log_dir):
        print(f"no archive dir: {log_dir}")
        return 3
    days = [os.path.join(log_dir, "requests-*.jsonl")]
    pairs = load_pairs(days, args.since)
    if not pairs:
        print("insufficient samples: 0 pairs（归档里还没有带 est_prompt_tokens 的行）")
        return 2
    s = summarize(pairs)
    print("samples=%d  median=%.2f  min=%.2f  p90=%.2f  max=%.2f"
          % (s["n"], s["median"], s["min"], s["p90"], s["max"]))
    by_model = {}
    for model, est, up in pairs:
        by_model.setdefault(model, []).append((model, est, up))
    for model, rows in sorted(by_model.items()):
        ms = summarize(rows)
        print("  %-28s n=%-4d median=%.2f p90=%.2f" % (model, ms["n"], ms["median"], ms["p90"]))
    if s["n"] < MIN_SAMPLES:
        print("insufficient samples: %d < %d，不裁决" % (s["n"], MIN_SAMPLES))
        return 2
    v = verdict(s)
    # 分桶只做「看得见」用，不参与裁决：小请求（代码/JSON 载荷密度低）天然会高估，
    # 混在大样本里会被中位数掩盖；这里单独列出，避免哪天它变成主要流量还没人发现。
    buckets = {"<10k": [], "10-50k": [], "50-200k": [], "200k+": []}
    for _, est, up in pairs:
        key = "<10k" if up < 10000 else "10-50k" if up < 50000 else "50-200k" if up < 200000 else "200k+"
        buckets[key].append((est, up))
    for key, rows in buckets.items():
        if not rows:
            continue
        bs = summarize([(None, e, u) for e, u in rows])
        print("  by upstream %-8s n=%-3d median=%.2f p90=%.2f" % (key, bs["n"], bs["median"], bs["p90"]))
    print(f"verdict: {v}")
    return 0 if v == "OK" else 1


if __name__ == "__main__":
    sys.exit(main())