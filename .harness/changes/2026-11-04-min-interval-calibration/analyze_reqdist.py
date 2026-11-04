import json, glob, datetime
from collections import defaultdict

files = glob.glob(r'D:\yg-gateway\data\request-logs\requests-*.jsonl')
rows = defaultdict(list)
total = 0
for f in sorted(files):
    for line in open(f, encoding='utf-8'):
        line = line.strip()
        if not line:
            continue
        o = json.loads(line)
        if o.get('kind') != 'chat':
            continue
        t = datetime.datetime.fromisoformat(o['time'])
        rows[o.get('account', '?')].append((t, o.get('status')))

def pct(xs, p):
    if not xs:
        return None
    xs = sorted(xs)
    return xs[int((len(xs) - 1) * p)]

def peak_per_min(ts):
    # 60s 滑动窗口最大请求数
    ts = sorted(ts)
    best = 0
    i = 0
    for j, t in enumerate(ts):
        while ts[i] < t - datetime.timedelta(seconds=60):
            i += 1
        best = max(best, j - i + 1)
    return best

print(f"{'account':<24}{'reqs':>6}{'429':>5}{'gap_min':>9}{'gap_p50':>9}{'gap_p95':>9}{'gap_max':>9}{'peak_req/min':>13}")
for acc, items in sorted(rows.items(), key=lambda kv: -len(kv[1])):
    items.sort(key=lambda x: x[0])
    ts = [t for t, _ in items]
    n429 = sum(1 for _, s in items if s == 429)
    gaps = [(b - a).total_seconds() for a, b in zip(ts, ts[1:]) if b > a]
    if not gaps:
        print(f"{acc:<24}{len(ts):>6}{n429:>5}{'--':>9}{'--':>9}{'--':>9}{'--':>9}{peak_per_min(ts):>13}")
        continue
    print(f"{acc:<24}{len(ts):>6}{n429:>5}{min(gaps):>9.1f}{pct(gaps,0.5):>9.1f}{pct(gaps,0.95):>9.1f}{max(gaps):>9.1f}{peak_per_min(ts):>13}")

# 全局按小时聚合，看每账号并发窗口
print("\n== 全量 ==")
allts = sorted(t for items in rows.values() for t, _ in items)
print("总请求:", len(allts), "账号数:", len(rows))
