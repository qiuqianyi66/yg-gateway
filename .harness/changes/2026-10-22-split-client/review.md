# 评审记录 — R-201 client.go 同包机械拆分（split-client）

- 日期：2026-10-22
- 复审身份：独立只读复审（子代理，L-16）。对象：commit `b85ea96`。
- 分级：L2（纯重构）。**本轮为第 1 轮独立复审。**

## 结论

**PASS**（0 🔴 / 0 🟡 / 2 🟢 / 2 🔵）

## 逐条 GWT 证据（REQ-split-client-2026-10-22）

| GWT | 代码级证据 |
|---|---|
| client.go 非空行降至 FILE_LIMIT 下 | `git show b85ea96 --stat`：client.go **-1573 纯删除**（无 insertions 列），新文件 errors.go/models_fetch.go/billing.go。summary 记 2058→556 非空行。合并后 main 实跑 `go build ./...` + `go test ./internal/upstream` 绿。 |
| 行为零变化（同包移动） | client.go 在本提交**只有删除行、无修改行**（stat 全是 `-`）——保留下来的端点路由/refresh/ChatStreamContext 未被改动。`go vet ./...` 无输出；gofmt 无输出。 |
| R8 报文字符串不动 | summary 明写未动 headers.go/payload.go/useragent*；main 上 `go test ./internal/upstream` 含 r8_golden_test 随全量绿。 |
| 表驱动/路由不改造 | 无注册表改动；import 按编译器裁剪（同包不产生新跨包方向）。 |

## 发现的问题

| 级别 | 问题 | 证据 / 处置 |
|---|---|---|
| 🟢 | "行为零变化"的唯一可验证性 = build+vet+test 同口径 + diff 纯删除/新增。无逐行 diff 比对工具，接缝处（errors.go 收尾、models_fetch.go 开头）靠编译接住 | 可接受：纯同包移动，编译器即接缝校验；测试同基线绿。记录为局限而非缺陷。 |
| 🟢 | depth-baseline.json 随提交收紧（maxFileLines 2058→558），属预期棘轮动作 | 正确方向，非问题。 |
| 🔵 | errors.go 558 / models_fetch.go 540 已接近 1000 线 | 未越线；提示下一簇职责再扩时优先新文件。 |
| 🔵 | 本变更无新增测试（纯移动），L-11 不要求新测试 | 正确：无行为变化，既有 upstream 测试即回归网。 |

## 对后续建议

无需返工。下一改 upstream 包时保持「职责簇 → 新文件」的切分习惯；R8 golden 测试应继续随每次改动跑。
