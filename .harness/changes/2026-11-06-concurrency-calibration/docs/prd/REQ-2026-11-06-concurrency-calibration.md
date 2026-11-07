# REQ — 单账号在途并发闸阈值校准（concurrency_per_account）

## 必要性

ADR-024 把并发闸机制落了地但默认 0 关，阈值不能凭空定：直接抄 magpie 的「5-6 即风控」
是无数据支撑的魔法数（L-26「校准≠机制」的另一面：机制在、阈值待观测）。而生产已有
8 天请求日志（time + duration_ms + account），一次离线分析就能给出「什么阈值不误伤
现有节奏、又能拦住异常爆发」——不做这次校准，机制就等于一直关着，等于没吸收。

## 需求

用 `D:\yg-gateway\data\request-logs\requests-*.jsonl` 复原每账号在途区间
`[time, time+duration_ms]`，统计到达时在途分布与时间曲线峰值，给出
`upstream.concurrency_per_account` 的建议值 + 各候选阈值的误伤率。

## 验收（Given-When-Then）

- Given 8 天日志（有账号归属请求 5998 条，8 个账号）→ Then 脚本一次跑通，输出
  每账号峰值表 + 全账号在途分位（p50/p90/p99/p99.9/max）+ 候选 N 误伤率表
  （已实跑：p99=2、峰值 4、N=4 误伤 0.000%）。
- Given 候选阈值 → Then 建议值须同时满足：误伤率可量化、不低于观测峰值、对照
  magpie 经验线给出取舍理由（已写：建议 4，理由见 summary）。
- Given 口径限制 → Then 显式标 unknown 而不是当事实（日志无 stream 字段，
  流式叠加无法拆开）。

## 结论

**建议 `concurrency_per_account: 4`（默认仍 0 关，由用户决定启用）。**
完整数据与脚本见同目录 `summary.md` / `analyze_concurrency.py`。
