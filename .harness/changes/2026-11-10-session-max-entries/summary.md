# 2026-11-10-session-max-entries 变更摘要

需求：`docs/prd/REQ-2026-11-10-session-max-entries.md`
分级：L2（业务逻辑：会话粘性存储的容量语义）

---

## 一、改了什么

| 文件 | 作用 / 改动 |
|---|---|
| `修改：internal/session/session.go` | 新增 `defaultMaxEntries=20000`、`Config.MaxEntries`、`maxEntries()`/`evictOldestLocked()`/`ensureCapacityLocked()`；三条插入路径（`ResolveForModel` 慢路径、`Bind`、`LoadFromStore`）插入前过容量检查，淘汰最久未活跃条目并镜像 `Store.DelBind` |
| `新增：internal/session/max_entries_test.go` | `TestRouterMaxEntriesEvictsOldest`（上限生效/淘汰最旧/重绑可用/覆盖不腾位）+ `TestRouterDefaultMaxEntries` |

**没做的（明写，免得下轮重提）**：
- 不加 `config.json` 字段（默认 3MB 上界已够；开配置项属配置堆积）。
- 不改 TTL / GCInterval，不做按 uid 的配额（与内存上限无关的另一议题）。

## 二、怎么验证的

| 验证 | 命令 | 结果 |
|---|---|---|
| 整包回归 + 覆盖 | `go test ./internal/session/ -count=1 -cover` | ok，coverage 98.5% |
| 静态检查 | `go vet ./internal/session` / `gofmt -l internal\session` | 退出码 0 / 无输出 |
| 体量棘轮 | `session.go` 行数 | 711 → 762，`maxFileLines` 1070 未触线 |
| 全量门禁 | `powershell -NoProfile -File scripts/pre-commit.ps1` | 11/11 通过（日志 `%TEMP%\yg-precommit-s3.log`） |

**负向探针**（副本 `%TEMP%\ygg-mut-s3`，已删除）：把 `ensureCapacityLocked` 的
`limit := r.maxEntries()` 变异成 `limit := 1 << 30`（等于上限失效）→ 用例报红
`Count=3 want 2（上限必须生效）`，按预期咬住。

## 三、未完成 / 待补（诚实清单）

- [ ] 淘汰是"单条淘汰"，不是"批量降到 90%"：稳态下每次新增都会多一次 O(n) 扫描。
      `requires`：先观测真实条目量级（`/status` 的 Count）与新增速率，再决定是否需要批量淘汰；
      当前 20000 条量级下单次扫描约微秒级，未测实际开销。
- [ ] `LoadFromStore` 恢复超限时淘汰顺序依赖 `lastActive=now` 相同值（map 迭代随机），
      未做确定化——恢复场景没有历史活跃度，属已知不可分辨。

## 四、怎么回滚

`git revert <提交>`：删掉三处 `ensureCapacityLocked` 调用与新增函数即可，绑定语义回到"仅 TTL"。
不涉及二进制/配置/凭证。

## 五、Learning

根因：拆包与重构时给"持久结构"配了 TTL，却没配容量——TTL 只管"多久没人用"，
不管"有多少人在用"，后者才是常驻进程的内存上界。
