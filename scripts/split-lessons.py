#!/usr/bin/env python3
# 一次性工具：把 .harness/LESSONS.md 的正文按 L-N 条目拆到 .harness/lessons/
# （REQ-harness-engineering-2026-10-19 §五 F3）。
#
# 为什么用脚本而不是手抄：LESSONS.md 366 行，手抄必然丢内容（LESSONS L-10 Seen 4，
# 本仓自己踩过 4 次「用拼接/替换构造文件内容 → 静默丢段落」）。脚本只做**按行切片**，
# 不做任何字符串改写，切完立刻逐条比对（见下面 verify），比对不过就非零退出。
#
# 用法：python scripts/split-lessons.py        # 只打印计划
#       python scripts/split-lessons.py -Apply # 落盘（幂等：已存在则覆盖）
#       python scripts/split-lessons.py -Verify # 只做回读比对

import io, os, re, sys

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
SRC = os.path.join(ROOT, '.harness', 'LESSONS.md')
DST = os.path.join(ROOT, '.harness', 'lessons')

def read_lines(p):
    with io.open(p, encoding='utf-8') as f:
        return f.read().split('\n')

def sections(lines):
    """返回 [(num, title, body_lines)]，以及 flake 段。"""
    starts = [i for i, l in enumerate(lines) if l.startswith('### L-')]
    flake_at = next((i for i, l in enumerate(lines) if l.startswith('## 二、已知 flake')), len(lines))
    out = []
    for k, s in enumerate(starts):
        e = starts[k + 1] if k + 1 < len(starts) else flake_at
        head = lines[s]
        m = re.match(r'^### (L-\d+)｜(.*)$', head)
        if not m:
            raise SystemExit('标题形态变了，脚本拒绝猜：' + head)
        body = lines[s:e]
        # 去掉尾部空行与分隔线
        while body and body[-1].strip() in ('', '---'):
            body.pop()
        out.append((m.group(1), m.group(2), body))
    flake = lines[flake_at:]
    while flake and flake[-1].strip() == '':
        flake.pop()
    return out, flake

def target(num):
    return os.path.join(DST, 'L-%02d.md' % int(num[2:]))

def head_lines(num, title):
    return [
        '# %s｜%s' % (num, title),
        '',
        '> 本条正文由 `.harness/LESSONS.md`（索引）外置而来，索引里那条 = 条目号 + 一句话 + Seen。',
        '> 更新正文时**同时**更新索引的「状态」列：`Seen ≥ 2` 必须已升级为规则或机器检查',
        '> （`scripts/check-harness.ps1` 检项 [7] 会把未升级的条目判为 error）。',
        '',
    ]

def render(num, title, body):
    return '\n'.join(head_lines(num, title) + body) + '\n'

def main():
    apply = '-Apply' in sys.argv
    verify = '-Verify' in sys.argv
    lines = read_lines(SRC)
    secs, flake = sections(lines)
    print('条目数 = %d，flake 段 %d 行' % (len(secs), len(flake)))

    if apply:
        os.makedirs(DST, exist_ok=True)
        for num, title, body in secs:
            with io.open(target(num), 'w', encoding='utf-8', newline='') as f:
                f.write(render(num, title, body))
        with io.open(os.path.join(DST, 'flake.md'), 'w', encoding='utf-8', newline='') as f:
            f.write('# 已知 flake（待修缺陷，不是流程教训）\n\n'
                    '> 由 `.harness/LESSONS.md` §二 外置而来。\n\n' + '\n'.join(flake[1:]) + '\n')
        print('已落盘 %d 个文件 + flake.md' % (len(secs) + 1))

    if apply or verify:
        bad = 0
        for num, title, body in secs:
            p = target(num)
            if not os.path.exists(p):
                print('缺文件: ' + p); bad += 1; continue
            got = read_lines(p)
            # 期望 = 头部 + 正文 + 结尾换行产生的空串
            want = head_lines(num, title) + body + ['']
            if got != want:
                print('内容不一致: ' + p)
                print('   got 行数=%d want 行数=%d' % (len(got), len(want)))
                bad += 1
        if bad:
            raise SystemExit('回读比对失败 %d 处——不要继续，先查脚本' % bad)
        print('回读比对通过：%d 条正文与源文件逐行一致' % len(secs))

if __name__ == '__main__':
    main()
