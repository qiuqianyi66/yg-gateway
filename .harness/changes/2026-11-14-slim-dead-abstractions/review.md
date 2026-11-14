# 评审记录：精简未接线抽象层 + 拆 main() 装配块

- 评审对象：`bd71c08`（第一批：删 quota/router/Registry + 合并护栏）与本轮第二批（main() 拆块）
- 评审方式：**自评审**（按 `.harness/skills/main-dev/expert-reviewer` 6 维）
- 证据来源：本会话实跑输出（见文末），非历史记录

> ⚠️ 流程缺口（显式记录，不隐藏）：`开发流程规范 §四` 要求 L2+ 改动有**一次独立复审**
> （另一会话 / 子代理 / 他人，只读），自评审不能作为唯一凭据。本轮**未执行独立复审**
> （用户未要求派子代理；单会话内视作自评审）。因此本文件的结论强度是「自评审通过 +
> 机械门禁全绿」，不是「独立复审通过」。后续若引入第二读者，应以本文件为待复核清单。

## 变更面

| 批次 | 文件 | 性质 |
|---|---|---|
| 一 | `internal/quota/`（删）、`internal/provider/*`、`internal/router/`（删）、`internal/pool/guards.go`（新）、`internal/pool/pick.go`、`internal/server/logging*.go`、`scripts/check-boundaries.ps1` | 删死代码 + 合并重复实现 |
| 二 | `cmd/server/main_wiring.go`（新）、`cmd/server/main.go` | 纯搬运：main() 483 → 354 行 |

## 六维评审

### 1. 正确性

| 检查点 | 结论 | 依据 |
|---|---|---|
| `router` 删除后选号顺序 | 等价 | `pickNonCredits` 构造的每个 `View` 恒 `Healthy:true`，`Order.Pick` 必返回 0；改 `cands[0]` 后仍是「map 迭代随机首个」 |
| `guards.go` 合并后判定语义 | 逐字等价 | 4 个闭包（realmOK / failureWindowBlocked / minIntervalBlocked / breakerHalfOpen）的判定式与边界注释整段迁移，`now` 仍在函数入口取一次 |
| `main()` 四块搬运 | 无行为变更 | 逐块删除行数 19 / 42 / 39 / 41，插入 3 / 4 / 2 / 5；`defer` 顺序（store/pool 关闭、rec.Stop、requestLog.Close、stop）全留在 main 未动 |
| `serveHTTP` 的错误路径 | 等价 | 原 `ListenAndServe` 的 `ErrServerClosed` 判定、30s 排水窗口、`p.Flush()` 前置顺序全部保留；错误改为 return 给 main Fatalf（原本就是 main 里 Fatalf） |
| 搬运期发现的口径错误 | **已修** | 首版把 `cfg.DegradeCooldownDur` 误写成 `cfg.Pool.DegradeCooldownDur`（该字段在 Config 顶层，Pool 里只有 `DegradeCooldown string`）→ 编译期即报错并修正 |

### 2. 性能

- 装配路径只在启动执行一次，`main()` 拆块不改变调用次数；`guards.go` 把 4 个闭包变成方法，栈帧形态等价（编译器内联），无新增分配。
- `pickNonCredits` 去掉 `[]router.View` 构造后少一次 slice 分配（该方法本就不是热路径，收益记为零头，不作为性能结论）。
- 🔵 提示：`pickerGuards` 传值（4 个字段 + 指针）比闭包捕获略多一次拷贝，量级可忽略，未做 bench 对比，故不作性能声明。

### 3. 安全

| 检查点 | 结论 |
|---|---|
| 是否引入新的输入解析路径 | 否（两批均为删除/搬运） |
| 凭证是否进入日志 | 否；`logScheduleBanner` 只打印调度启停，无凭证字段 |
| `serveHTTP` 是否改变暴露面 | 否；监听地址仍取 `cfg.Listen`，无新增路由 |
| R1 / R2 / R5 / R8 | 未触碰 |
| `clientIPForLog` 删除 | **降低**风险：该函数注释写明「历史语义，无条件信任代理头」，生产已改用 `extractClientIP`，删除避免了被误用 |

### 4. 一致性

- 新文件名 `main_wiring.go` 与既有 `provider_wiring.go` / `opsagent_wiring.go` / `keyalert_wiring.go` 同族命名 ✓
- `guards.go` 与 `pick.go` 的注释口径一致（ADR-003 D-400 / ADR-005 D-500 编号保留）✓
- 行数口径：本文件与本轮汇报一律区分「实际行」与「非空行」（LESSONS L-5）✓

### 5. 可维护性

- `main()` 从 483 → 354 行（−27%）；`main.go` 非空行 1070 → 942，**退出 >1000 名单**。
- 代价：总量非空行 38657 → 38708（**+51**），因为 `main_wiring.go` 新增了 3 段函数级 why 注释。这是有意的交易：局部 locality 换少量注释成本。
- 🟢 保留问题：`applyUpstreamConfig` 里含 `auth.SetGlobalEnabled(cfg.Global.Enabled)`（auth 侧开关），函数名只覆盖 upstream。注释已说明「auth 侧开关是第二道闸，两者同 config global.enabled」，故不拆（拆开会让「global 开关」这一个语义分处两地）。
- 🟢 保留问题：`main()` 仍 354 行。继续拆需要把 panel/handler 的 30+ 个局部变量变成参数传递，会让「装配上下文」更碎，判定收益为负，**主动停止**。

### 6. 架构合规

| 检查 | 结果 |
|---|---|
| 依赖方向（`check-boundaries.ps1`） | ✅ 通过（allow 表已同步移除 quota / router 条目） |
| 分层（cmd 内的装配块不反向被 internal 引用） | ✅ `main_wiring.go` 在 cmd/server，无 internal 包 import 它 |
| 四态纪律 | ✅ 无新对外事实字段 |
| R13（前端改动需重编译） | 未涉及前端 |
| R10（错误不静默吞） | 删除的函数里无 `_ =` 模式；`serveHTTP` 的错误向上返回而非吞掉 |

## Findings 汇总

| 级别 | 条目 | 处置 |
|---|---|---|
| 🔴 | 无 | — |
| 🟡 | 无 | — |
| 🟢 | `applyUpstreamConfig` 含 auth 侧开关（见 §5） | 记录，注释兜住 |
| 🟢 | `main()` 仍 354 行，继续拆收益为负 | 记录，主动停止 |
| 🔵 | `pickGuards` 传值拷贝 | 记录，未实测故不作结论 |
| ⚠️流程 | 独立复审未执行（见文首） | 记录，结论强度降级为「自评审 + 门禁全绿」 |

## 机械证据（本会话实跑）

```
go build ./...                      exit 0
go vet ./...                        exit 0（无输出）
go test ./... -count=1              26 包全绿（cmd/credit…internal/usage）
gofmt -l cmd internal               无输出
scripts/check-boundaries.ps1        ✅ 通过
scripts/pre-commit.ps1 -Fast -AllFiles -Base v2.1.0   11 项全过（第一批提交时）
scripts/check-depth.ps1             maxFileLines 1070 / >1000 文件 2 / >120 行函数 7 / 覆盖率 75.5%
```
