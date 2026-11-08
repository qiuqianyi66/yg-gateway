# REQ — rhythm 桶名可读化（/status 里能对上账号）

## 必要性

F1 的 `/status` `rhythm` 键按 `X-Machine-ID`（36 hex）分桶。生产启用 `conc=4` 后实测
三个桶两个是哈希、一个 `global`——运维只能数个数，无法判断「是哪个号被打热 / 被排队」。
F1 的差异化卖点正是「per-account 可观测」（magpie 自承 unknown），哈希桶名把这个卖点
打折成「有数据但用不上」。可读化不是美化，是让观测能真正支撑决策。

## 需求

`/status` 的 `rhythm` 桶名在匹配到账号时显示为 `昵称(uid8)`（昵称空则仅 `uid8`）；
`global` 与无法匹配的桶原样保留。分桶语义、桶数量、`in_flight/queue_depth` 数值不变。

## 验收（GWT）

- Given `uid=0250aab9-... nickname=🐻` 的桶 → When 读 `/status` → Then 桶名为
  `🐻(0250aab9)`，数值与改前一致。
- Given `global` / 未知 36 hex 桶 → Then key 原样保留（未知不隐藏）。
- Given 账号无昵称 → Then 桶名为 uid8。
- Given `MachineIDForUID(uid)` → Then 与 `injectAccountStableHeaders` 注入的
  `X-Machine-ID` 逐字节相等（同源派生，不漂移）。

## 不做

- 不在 `RhythmStatus` 里带昵称（upstream 不该知道账号昵称）；不改上游分桶 key
  （machine-id 是出站风控面的原生粒度，与 rpm 分桶同口径）。
