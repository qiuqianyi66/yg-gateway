# analyze_rpm.py — RPM 硬闸启用阈值校准（对 8 天生产请求日志做分布分析）
# 用法：python analyze_rpm.py [logs_dir]
# 输出：每账号 60s 滑动窗峰值 / 1s 槽位占用峰值 / 间隔<1s 的请求对数
# 判据（对照 internal/upstream/rpm.go 语义）：
#   - 60s 窗内最多 rpmPerAccount 个槽位；窗口满 → 阻塞等待（加延迟，不失败）
#   - 每槽位占用 1s（请求间最小间隔）→ 间隔 <1s 的请求会被拉到 1s
# 误伤判据：观测到的真实峰值超过候选阈值 N → 开启 N 会引入额外等待
import json, glob, sys, datetime
from collections import defaultdict

logs_dir = sys.argv[1] if len(sys.argv) > 1 else r'D:\yg-gateway\data\request-logs'
files = sorted(glob.glob(logs_dir + r'\requests-*.jsonl'))
rows = defaultdict(list)
total = 0
for f in files:
    for line in open(f, encoding='utf-8'):
        line = line.strip()
        if not line:
            continue
        o = json.loads(line)
        if o.get('kind') != 'chat':
            continue
        t = datetime.datetime.fromisoformat(o['time'])
        rows[o.get('account', '?')].append(t)
        total += 1

def peak_in_window(ts, seconds):
    ts = sorted(ts)
    best = 0
    i = 0
    for j, t in enumerate(ts):
        while ts[i] < t - datetime.timedelta(seconds=seconds):
            i += 1
        best = max(best, j - i + 1)
    return best

def peak_same_second(ts):
    # 1s 槽位占用峰值：落在同一秒内（或间隔 <1s）的请求数——RPM slot 会强制拉平
    ts = sorted(ts)
    best = 0
    i = 0
    for j, t in enumerate(ts):
        while ts[i] < t - datetime.timedelta(seconds=1):
            i += 1
        best = max(best, j - i + 1)
    return best

print(f"{'account':<24}{'reqs':>6}{'peak60s':>9}{'peak1s':>7}{'pairs<1s':>9}")
for acc, ts in sorted(rows.items(), key=lambda kv: -len(kv[1])):
    ts_sorted = sorted(ts)
    pairs = sum(1 for a, b in zip(ts_sorted, ts_sorted[1:]) if (b - a).total_seconds() < 1.0)
    print(f"{acc:<24}{len(ts):>6}{peak_in_window(ts, 60):>9}{peak_same_second(ts):>7}{pairs:>9}")

allts = sorted(t for ts in rows.values() for t in ts)
print(f"\n全量 chat 请求: {total}，账号数: {len(rows)}，日期文件: {len(files)} 天")
print(f"全账号 60s 峰值上限: {peak_in_window(allts, 60)}，1s 峰值上限: {peak_same_second(allts)}")

# 全 kind 维度：RPM 硬闸作用于全部出站（不只 chat），补算不过滤的分布
allrows = defaultdict(list)
for f in files:
    for line in open(f, encoding='utf-8'):
        line = line.strip()
        if not line:
            continue
        o = json.loads(line)
        t = datetime.datetime.fromisoformat(o['time'])
        allrows[o.get('account', '?')].append((t, o.get('kind')))
akind = [t for ts in allrows.values() for t, _ in ts]
kinds = sorted({k for ts in allrows.values() for _, k in ts})
print(f"\n全 kind 请求: {len(akind)}，账号数: {len(allrows)}，kind 分布: { {k: sum(1 for ts in allrows.values() for _, kk in ts if kk == k) for k in kinds} }")
print(f"全 kind 60s 峰值上限: {peak_in_window(akind, 60)}，1s 峰值上限: {peak_same_second(akind)}")
print(f"\n== 每账号全 kind 峰值（含非 chat） ==")
print(f"{'account':<24}{'reqs':>6}{'peak60s':>9}{'peak1s':>7}")
for acc, items in sorted(allrows.items(), key=lambda kv: -len(kv[1])):
    ts = [t for t, _ in items]
    print(f"{acc:<24}{len(ts):>6}{peak_in_window(ts, 60):>9}{peak_same_second(ts):>7}")
