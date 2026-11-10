# 2026-11-10-blackcat-bypass 变更摘要

需求：`docs/prd/REQ-2026-11-10-blackcat-bypass.md`
分级：L2（P3：面板旁路导致同族重复的真实模型对话，量小、有界）

## 一、改了什么

| 文件 | 作用 / 改动 |
|---|---|
| `修改：internal/scheduler/scheduler.go` | `Scheduler` 新增 `gateBusy [taskGateSlots]atomic.Bool`，由 `gate` 取锁置 true、释放置 false；新增导出方法 `BlackcatBusy()` 只读查询（`sync.Mutex` 无「是否持有」查询，故单列标记） |
| `修改：internal/panel/autotask_scripts.go` | 新增可替换窗口时钟 `nowForBlackcat`（与 scheduler 的 `nowForNightWindow` 同模式）；`runBlackCat` 在窗口守卫之后、`BlackcatNeed` 之前查 `BlackcatBusy`，排程在跑即返回提示，不发上游调用 |
| `新增：internal/scheduler/task_gate_test.go（追加用例）` | `TestBlackcatBusyFlagReflectsGate`：钉夜间窗口 + 请求阻塞，断言取锁后 busy=true、释放后 false |
| `新增：internal/panel/blackcat_bypass_test.go` | `TestRunBlackCatSkipsWhileSchedulerBusy`：真实排程 + 面板叠窗口，断言跳过提示且面板侧零调用；墙钟在窗口外时 Skip（排程时钟在 scheduler 包内不可替换，不伪造） |

## 二、怎么验证的

| 验证 | 命令 | 结果 |
|---|---|---|
| 新用例 | scheduler 用例 `-count=3` | 全绿；面板用例当前墙钟窗口外 Skip（夜间窗口内可跑） |
| 整包 | `go test ./internal/scheduler/ ./internal/panel/ -count=2` | 全 ok（39s / 113s） |
| 静态 | `go build ./...`、`go vet`、`gofmt -l` | 0 / 无输出 |
| 全量门禁 | `scripts/pre-commit.ps1` | 11/11（日志 `%TEMP%\yg-precommit-p3.log`） |
| 负向探针 | 副本变异：取锁后不置忙标记 | 0.02s 报红 `取锁后 BlackcatBusy=false want true`（副本已删） |

## 三、设计取舍（明写）

- 忙标记是**建议性**的，存在极小的「标记已清、锁未放」窗口；后果只是罕见重复（不劣于修复前），
  不引入新风险。没用「再 TryLock 一次探活」的做法——那会真的抢锁、改变 P2-5 语义。
- 面板用例不强行替换 scheduler 的窗口时钟：跨包不可达，宁可夜间窗口外 Skip 也不伪造排程状态。

## 四、怎么回滚

`git revert <提交>`：删掉 `gateBusy`/`BlackcatBusy`/面板的检查与时钟变量即回到修复前；
不涉及配置、凭证、二进制。

## 五、验证缺口（unknown）

- 面板端到端用例今晚窗口外 Skip，真实叠窗的动态验证留待 23:00 后；scheduler 侧机制已闭合。
- `-race` 本机无 gcc，靠 CI。
