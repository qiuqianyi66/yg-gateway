# REQ — 会话绑定条目上限（2026-10-22 审查 S-3）

## 必要性

`internal/session` 的 `entries` map 只有两条出口：TTL 过期（`gcOnce`，默认 30m）与 `Unbind`。
key 由客户端 `conversationId` 派生——**用不完的唯一 ID 就能让 map 无界增长**：
每条约 100–150 字节，长期运行 + 爬虫式流量下会慢速吃内存，且不报错、不告警，
症状只是"跑久了 RSS 变大"。对 7×24 常驻单机网关，这是唯一没有上界的常驻数据结构之一。

## 需求

`Config` 增加 `MaxEntries`（<=0 回落 `defaultMaxEntries = 20000`）。三条插入路径
（`ResolveForModel` 慢路径分配、`Bind`、`LoadFromStore`）都过 `ensureCapacityLocked`：
条目数达到上限时先淘汰 `lastActive` 最旧的一条（并镜像 `Store.DelBind`），再插入。

## 验收（GWT）

- Given `MaxEntries=2` 且已绑 k1（人为把 lastActive 推到 1 小时前）→ When 再绑 k2、k3
  → Then `Count()==2` 且 k1 被淘汰（最久未活跃先走）、k2/k3 存活。
- Given 已淘汰的 k1 → When 重新 `Bind` → Then 绑定成功（淘汰 ≠ 拉黑）。
- Given 覆盖已有 key → Then 容量不变（不触发额外淘汰）。
- Given 未配 `MaxEntries` → Then `maxEntries()` 返回 20000（配置漏传不是无界）。
- Given 去掉上限（变异 `limit := 1<<30`）→ Then `Count=3 want 2` 报红。
- Given 全量门禁 → Then `pre-commit.ps1` 退出码 0。

## 不做

- 不加 `config.json` 字段：默认 20000 已把最坏情况钉在约 3MB，为它开配置项属"配置堆积"；
  将来真需要调再按实测证据加。
- 不改 TTL / GCInterval 语义，不改淘汰判据以外的行为（加速淘汰老会话会让多轮对话更容易
  跳号，属另一议题）。
- 不做按 uid 维度的配额（同一账号被过多会话绑定的问题与内存上限无关）。
