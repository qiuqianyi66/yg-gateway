# 计划 — rhythm 桶名可读化（2026-11-08）

## 必要性

F1 落地后 `/status` 的 `rhythm` 键按 `X-Machine-ID`（`sha256("wb2a:machine:"+uid)` 前 36 hex）
分桶。生产实测三个桶两个是 36 hex 乱码、一个 `global`——运维能看「有几个桶、各自在途多少」，
**看不出是哪个账号**，等于把「可审计定位」这个 F1 的差异化卖点打折。可读化不是美化：
判断「某号被打热/被排队」必须能对上账号，否则只能靠猜。

## 方案（最小改动，不动分桶语义）

1. `internal/upstream/concurrency.go` 导出 `MachineIDForUID(uid)`——复用
   `deriveAccountStableID(uid, "machine")`，保证与 `injectAccountStableHeaders` 同源
   （不是另算一套哈希）。
2. `internal/server/status.go` 的 `rhythmSnapshot()` 增加重命名层：
   `rhythmRelabel(raw, h.cfg.Pool.List())`，把已匹配账号的桶名换成 `昵称(uid8)`
   （昵称空 → 仅 `uid8`）。未匹配的桶（`global` / 无账号归属请求）**原样保留**
   （不隐藏、不合并，未知就是未知）。
3. 上游 `RhythmStatus()` 的分桶 key **不改**：machine-id 是出站风控面的原生粒度，
   改了会和 rpm 分桶口径不一致。

## 验收（GWT）

- Given 账号 `uid=0250aab9-... nickname=🐻` → When `/status` 输出 rhythm → Then 桶名为
  `🐻(0250aab9)`，`in_flight/queue_depth` 数值不变。
- Given `global` 桶与未知 36 hex 桶 → Then 两者 key 原样保留。
- Given `MachineIDForUID("u1")` → Then 与 `injectAccountStableHeaders` 写入的
  `X-Machine-ID` 完全相等（同一派生，不漂移）。

## 测试牙齿

- `TestMachineIDForUIDMatchesHeader`：篡改派生盐或截断长度 → 红。
- `TestRhythmRelabelReadable`：删掉重命名分支（直接 return raw）→ 红。

## 承重墙 / 红线

- 不动 `concurrencyWait` 的 key 选择、不动 `RhythmStatus` 分桶逻辑（只改输出层）。
- 零回归：`rhythm` 键恒存在；空表仍 `{}`；不改任何数值语义。
- 不改面板（前端无 rhythm 消费）；若后续要显示，读 key 即可。

## 不做

- 不做 uid→桶的反向持久化映射表（每次请求现算，O(账号数)=8，忽略不计）。
- 不在 `RhythmStatus` 里带 nickname（upstream 不该知道账号昵称，职责越界）。
