# analyze_concurrency.py — 单账号在途并发闸（concurrency_per_account）阈值校准
# 用法：python analyze_concurrency.py [logs_dir]
# 口径：
#   - 在途区间 = [time, time + duration_ms]（日志 time 为请求开始时刻，duration_ms 为总耗时）
#   - 并发闸挂在出站 Transport 层，故只统计真正有账号归属的请求（account != '-'；
#     '-' 是未选号的本地拒绝/代理路径，根本没出站）
#   - 同一时刻的事件先处理 -1（结束）再处理 +1（开始），避免把「刚结束」误算在途
# 判据（对照 internal/upstream/concurrency.go 语义）：
#   - 每账号在途 ≤ N；满则新请求入队等待（加延迟，不失败）
#   - 误伤率 = 到达时在途 ≥ N 的请求占比（这些请求会被排队）
# 结论形态：给「候选 N → 误伤率」表，取误伤 <1% 的最小 N，再对照 magpie 经验（5-6 即风控）
import json, glob, sys, datetime
from collections import defaultdict

logs_dir = sys.argv[1] if len(sys.argv) > 1 else r'D:\yg-gateway\data\request-logs'
files = sorted(glob.glob(logs_dir + r'\requests-*.jsonl'))


def pct(sorted_vals, q):
    if not sorted_vals:
        return 0
    i = min(len(sorted_vals) - 1, max(0, int(round(q * (len(sorted_vals) - 1)))))
    return sorted_vals[i]


rows = defaultdict(list)  # account -> [(t_start, t_end, kind, status)]
total_all = 0
for f in files:
    for line in open(f, encoding='utf-8'):
        line = line.strip()
        if not line:
            continue
        o = json.loads(line)
        total_all += 1
        acc = o.get('account') or '-'
        if acc == '-':
            continue
        t0 = datetime.datetime.fromisoformat(o['time'])
        dur = o.get('duration_ms') or 0
        t1 = t0 + datetime.timedelta(milliseconds=dur)
        rows[acc].append((t0, t1, o.get('kind'), o.get('status')))

print(f"日期文件 {len(files)} 天，日志总行 {total_all}，有账号归属请求 "
      f"{sum(len(v) for v in rows.values())}，账号数 {len(rows)}\n")

CAND = [2, 3, 4, 5, 6, 8]
all_arrival = []  # 全账号：到达时在途样本
all_peak = 0
print(f"{'account':<26}{'reqs':>6}{'peak':>6}" +
      "".join(f"{'≥' + str(c):>8}" for c in CAND))
for acc, items in sorted(rows.items(), key=lambda kv: -len(kv[1])):
    # sweep：事件 (t, +1/-1)，同刻 -1 先处理
    ev = []
    for t0, t1, _, _ in items:
        ev.append((t0, +1))
        ev.append((t1, -1))
    ev.sort(key=lambda x: (x[0], x[1]))  # (t, -1) 在 (t, +1) 之前
    inflight = 0
    peak = 0
    arrivals = []
    for _, delta in ev:
        if delta == -1:
            inflight -= 1
        else:
            arrivals.append(inflight)  # 本请求到达时的在途数（不含自己）
            inflight += 1
            peak = max(peak, inflight)
    all_arrival.extend(arrivals)
    all_peak = max(all_peak, peak)
    hi = {c: sum(1 for a in arrivals if a >= c) for c in CAND}
    print(f"{acc:<26}{len(items):>6}{peak:>6}" +
          "".join(f"{hi[c]:>8}" for c in CAND))

arrival_sorted = sorted(all_arrival)
n = len(arrival_sorted)
print(f"\n全账号到达时在途分布（{n} 样本）: "
      f"p50={pct(arrival_sorted, .50)} p90={pct(arrival_sorted, .90)} "
      f"p99={pct(arrival_sorted, .99)} p99.9={pct(arrival_sorted, .999)} "
      f"max={arrival_sorted[-1] if arrival_sorted else 0}；时间曲线峰值={all_peak}")

print(f"\n{'候选N':>6}{'误伤请求数':>12}{'误伤率':>10}")
for c in CAND:
    bad = sum(1 for a in all_arrival if a >= c)
    print(f"{c:>6}{bad:>12}{bad / n * 100:>9.3f}%")

# 每账号峰值表（判断账号间差异，避免一刀切）
print("\n每账号时间曲线峰值（≥3 的账号才列出）:")
for acc, items in sorted(rows.items(), key=lambda kv: -len(kv[1])):
    ev = []
    for t0, t1, _, _ in items:
        ev.append((t0, +1))
        ev.append((t1, -1))
    ev.sort(key=lambda x: (x[0], x[1]))
    inflight = peak = 0
    for _, delta in ev:
        inflight += delta
        peak = max(peak, inflight)
    if peak >= 3:
        print(f"  {acc:<26} peak={peak} reqs={len(items)}")
