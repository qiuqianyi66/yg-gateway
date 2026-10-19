# 变异核验 — reqlog 写入侧 + request_logs 接口侧（2026-10-19 第三批）

- 日期：2026-10-19
- 分级：**L1**（只加核验资产；**没有新增测试**——两批变异首轮即被既有测试咬住）
- 新增清单：`server-reqlog-write`（6）+ `panel-reqlog`（5）共 **11 条**
- 入口：`scripts/mutation/run-all.ps1`

## 一、为什么补这两处

上一轮收口时明确记了两条 unknown：

1. reqlog **写入侧**（轮转 / 保留天数 / 总容量 / 丢弃计数）只有读取侧断言；
2. `/panel/api/request_logs` 的 **HTTP 接口侧**（limit 口径、筛选参数透传、空结果形状）没进清单。

这两处都是「出问题时不会报错、只会悄悄丢数据或悄悄返回错数据」的位置，正是变异核验该管的。

## 二、`server-reqlog-write`（internal/reqlog，6 条）

| id | 变异 | 想钉住的行为 |
|---|---|---|
| W1 | 队列满丢弃不计数 | `DroppedWrites` 必须反映真实丢弃量（不然运维以为没丢） |
| W2 | 单文件大小上限失效 | `FileMaxBytes` 触发轮转（否则单文件无限增长） |
| W3 | 保留天数不裁切 | `RetentionDays` 过期即删 |
| W4 | 总容量上限不裁切 | `MaxBytes` 封顶 |
| W5 | 关停时不排空队列 | 进程退出前把 channel 里的事件写完 |
| W6 | 删除后不递减总量 | 按旧总量继续删会造成**过度清理** |

**首轮**：5 咬住 / 1 无效变异。W3 的第一版把 `cutoff` 改成未使用变量 → `build failed`，
driver 按规则判「无效变异」（不是咬住）。改成弱化形态
`it.mtime.Before(cutoff) && total > MaxBytes`（过期且超容量才删）后 **6/6 咬住**。

## 三、`panel-reqlog`（internal/panel，5 条）

| id | 变异 | 想钉住的行为 |
|---|---|---|
| H1 | 默认 limit 200 → 20 | 面板默认页的条数口径 |
| H2 | limit 上限 1000 不夹 | 大 limit 不得把整个归档读进内存 |
| H3 | `outcome` 参数不透传 | 选「失败」必须只返回失败 |
| H4 | 空结果回 `null` | 必须回 `[]`（前端把 null 当「归档关闭」） |
| H5 | `from` 参数不透传 | 「今天 / 自定义区间」查询必须真的限定区间 |

**首轮 5/5 咬住**（`TestRequestLogsEmptyRangeReturnsArray` 等既有用例覆盖到位）。

## 四、结论

两批 11 条**全部被既有测试咬住，没有发现新缺口**——这一轮的产出是把两条 unknown
降级为「有测试盯着的已知」，并让它们进入一条命令的复跑基线。

## 五、复现

```powershell
powershell -NoProfile -File scripts/mutation/run-all.ps1 -Only reqlog   # 两个清单都命中
powershell -NoProfile -File scripts/mutation/run-all.ps1 -Fresh        # 全量
```

## 六、仍未覆盖（继续按 unknown 记）

- `DesktopChatWithExpert` 的真实 chat 链路（只覆盖了召唤上报失败）；
- `RecordTokenUsage` 延迟样本的长序列（只钉首观测与罚分封顶）；
- driver 的 `(cached) ⇒ 无效变异` 判据仍未被真实缓存命中触发。

## 七、全量复跑与一次输入事故（本轮重要副产品）

最终 `run-all.ps1 -Fresh`：**16 清单 / 90 条变异 → 90 咬住 / 0 存活 / 0 无效，exit 0**。

中间有一次复跑报「异常清单 9 个」，每行都是「（无汇总行）」——**不是 9 个清单有存活变异，是
`%TEMP%` 副本在跑到后半程时损坏了**（只剩 `internal/`，`go.mod` 与 `.git` 消失），后续清单都在
基线处 `go env GOMOD=NUL` 失败。聚合层把「输入坏了」和「变异存活」显示成同一句话，差点误判。

已修（`run-all.ps1`）：

1. 跑任何清单前先证副本完整（`go.mod` + `.git` + `git rev-parse`），坏了**自动重建并复检**；
2. 子脚本非 0 且无汇总行时**打印它的输出尾巴**；
3. 副本为什么会在长跑中途丢文件**未定位**（unknown），按 LESSONS L-19 记 Seen 1，第 2 次再现升级为每清单哈希校验。

修复后同一路径复跑 90/90 全绿，期间观察副本完好。
