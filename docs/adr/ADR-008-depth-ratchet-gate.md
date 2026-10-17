# ADR-008：深度门禁用「棘轮 + 自建 PowerShell 脚本」，不引入 linter / 覆盖率工具

- 状态：已采纳
- 日期：2026-10-16
- 相关：`scripts/check-depth.ps1`、`scripts/check-boundaries.ps1`、`scripts/pre-commit.ps1`、
  `.github/workflows/release.yml` 的 `gate` job（2026-10-16 两条发布线合并前是
  `go-binaries.yml` / `docker-ghcr.yml` 各调一次）、`.harness/rules/编码规范.md` 第九节、
  `.harness/rules/开发流程规范.md` 第五节、`.harness/changes/2026-10-16-ci-gate/summary.md`

## 背景

2026-10-15 的实测基线：`chatCompletions` 单函数 **871 行**、最大文件 **2185 非空行**、
核心层最低包覆盖率 **56.7%**、`panel` 只有 13.6%、gofmt 漂移 4 个文件、静默忽略错误 121 处。
这些数字**不是没写在文档里**——`.harness/rules/` 早就写着「函数要拆、文件不要太大」，
结果是「文档里写着要拆但没人拦」。**没有退出码的规范等于没有规范**。

同时又不能随便上工具：

1. **零新依赖是本仓的定位硬约束**（`go.mod` 直接依赖只有 `github.com/redis/go-redis/v9`，
   单二进制 `go:embed` 分发）。引入 golangci-lint 体系会带来：工具链版本漂移、
   CI 加一份 `go install`、以及**大量默认规则与既有代码风格冲突**（本仓的注释文化、
   `_ = f()` 的显式忽略判据都会被打成告警）。
2. **绝对阈值不可行**：存量债（871 行函数、2185 行文件）一次性全红，现实中的结局是
   「把阈值调到刚好通过」，门禁变成装饰。
3. **覆盖率≠有牙齿**：本轮在 4 个小包上用变异测试实测出真洞（`prompt.Degraded` 的
   断言自证循环、`Rewrite` 丢 `tools` 无人钉、`redisstore` 删掉并发上限全绿）——
   覆盖率涨了不等于行为被钉住了。

## 决策

### 1. 棘轮（ratchet）语义，而不是绝对阈值

`scripts/check-depth.ps1` 维护四项指标（`maxFileLines` / `oversizeFiles` / `oversizeFuncs` /
`minCoverage`），与入库的 `.harness/depth-baseline.json` 对比：

- 只要求「**不许比上次更差**」（覆盖率带 0.5pp 容差，吸收 map 遍历顺序等抖动）；
- 存量债不阻塞提交，**新增债立即拦截**；
- 指标变好时用 `-Update` **主动抬高**棘轮（冻结收益），但不许用它掩盖回退；
- **只许收紧。** 因口径修正需要重算基线时，必须在变更追踪里给出「真阳性保留 + 假阳性消除」的证据。

### 2. 度量、边界与提交前检查都自建 PowerShell 脚本

| 脚本 | 职责 | 强制点 |
|---|---|---|
| `check-depth.ps1` | 四项深度指标 + 棘轮 | 本地 + CI `gate` |
| `check-boundaries.ps1` | 依赖方向白名单（实测生成的冻结基线） | 本地 + CI `gate` |
| `pre-commit.ps1` | 改动文件 gofmt / R10 新增静默忽略 / build / vet / test / 上面两项 | 本地总闸；CI `gate` 用 `-Fast -AllFiles -Base <ref>` 复用同一实现 |

**门禁必须有两层，且 CI 是最终闸门**：本地 `pre-commit.ps1` 只是「提前发现」——
`--no-verify`、忘记跑、或别的会话直接 push 都能绕过；而 PR 的 `paths` 过滤器还曾经
**不含 `scripts/**`**，等于「改门禁不需要门禁验」。现已把 `gate` job 接入 CI，
并让 `build` job `needs: [test, gate]`（门禁红则不产出二进制）。

### 3. 明确不引入的东西

- 不引入 golangci-lint / staticcheck（理由见下「取舍二」）；
- 不引入 codecov / gocov 等覆盖率服务：覆盖率只用标准库 `go test -cover`；
- **变异测试不入门禁**：它需要「改生产代码 + 跑全量 + 还原」，无法在提交门禁的时间预算内完成，
  作为**一次性核验手段**使用（本轮 T3 正在对 `upstream` / `panel` 做这件事），
  结论以「哪些行为已被钉住」的形式落盘。

## 关键取舍

### 取舍一：棘轮 vs 绝对阈值

棘轮的代价是**基线文件成了必须维护的状态**：数字与口径一旦含糊，就会出现
「2185（脚本值）与 2315（真实行）并存而无人知道哪个是哪个」这种事（本轮真实发生，
已在 `check-depth.ps1` 头部补「口径声明」并在 `编码规范.md` 第九节给出两种口径对照）。
收益是**门禁敢开**：一个永远全红的门禁不被信任，一个只拦新增债的门禁会被真正执行。

### 取舍二：自建脚本 vs golangci-lint —— 代价要如实说

自建脚本省掉了依赖与生态冲突，但**把工具本该负责的事搬到了自己身上**，本轮为此付了两次学费：

1. **口径要自己声明**：`Measure-Object -Line` 不计空行，脚本对函数口径写了 why、对文件口径
   一句没写 → 文档与脚本数字不一致。修法：显式过滤空白行（`Where-Object { $_.Trim() -ne '' }`），
   让口径与实现一一对应，不押在 cmdlet 的行为上。
2. **跨平台要自己保证**：`Join-Path $root '.harness\depth-baseline.json'` 在 Linux 上不是路径分隔，
   会让脚本找不到基线 → 走「初始化基线并 `exit 0`」分支 → **CI 里的棘轮永远假绿**。
   这比没有门禁更糟：它产出一份「已验证」的假证据。已改为分段 `Join-Path`。

**如果将来本仓放弃零依赖定位，这些成本应当重新评估**——这是本 ADR 唯一容易被推翻的前提。

### 取舍三：CI 里「本次改动」不能用工作区 diff

本地门禁看「工作区 + 暂存区 + 未跟踪」，而 CI 的工作区是干净的（改动早已提交），
照搬会让「改动文件清单」与 R10 都变成「检查了 0 个文件」的空转。
因此 `pre-commit.ps1` 增设 `-Base <ref>`：CI 传 `origin/<base>`（push 用 `HEAD~1`），
R10 与改动清单都按 `git diff <ref>...HEAD` 算；gofmt 用 `-AllFiles` 查全仓。
**同一个脚本、两套作用域参数**，而不是维护两份实现（避免口径分叉）。

> **2026-10-17 更新（A1）**：上面的「push 用 `HEAD~1`」已不成立。push 改用**事件里的
> `github.event.before`**（本次推送之前的 HEAD），tag push 用**上一个发版 tag**
> （`--match 'v[0-9]*' --exclude '*-ci'`），两者都不可用时才回落 `HEAD~1` 并打 `::warning::`。
> 原因：固定 `HEAD~1` 让一次推 N>1 个提交时只有最后一个进入差分（R10 与改动文件都是盲区）。
> 见 `docs/prd/REQ-ci-gate-base-2026-10-17.md` 与 `.harness/changes/2026-10-17-ci-gate-base/`。

### 取舍四：`-race` 由 CI 独占

本机（Windows）无 gcc，`go test -race` 跑不了。CI 用独立 `race` job（`CGO_ENABLED=1`），
与 `test`/`gate` 并行，不污染普通测试的 `CGO_ENABLED=0` 口径。本地并发正确性靠设计 + 单测保证。

## 后果

**正面**
- 深度从主观词变成 4 个可比数字：`2185 / 5 / 7 / 71.9%`（2026-10-16 基线），
  每次改动都能回答「变好还是变差」。
- 门禁改动本身也被门禁验（`paths` 已含 `scripts/**`）。
- 覆盖率与棘轮同一实现本地/CI 复用，口径不会分叉。

**负面 / 成本**
- 四份需要人维护的东西：基线 json、三份脚本、编码规范第九节的阈值表——**改一侧必须同步另一侧**。
- `gate` job 内部仍要跑一次带覆盖率的全量测试（棘轮需要实测覆盖率），与 `test` job 有约一次
  测试的重复耗时（Linux 上约 2 分钟）。
- 棘轮只管「不比上次差」，**对已存在的 5 个超大文件、7 个超长函数无作为**——
  这部分需要单独的重构计划（下一阶段），门禁只保证不再变坏。

## 验证（硬证据）

- **四件套与门禁基线**（2026-10-16，全绿）：`go build`/`go vet` 0 error；`gofmt -l` 空；
  `go test ./... -count=1` 23 包（20 ok + 3 no test files）；`check-boundaries` 0；
  `check-depth` 0（`2185 / 5 / 7 / 71.9%`）；`pre-commit.ps1` 全量 0。
- **口径替换的等价性**：文件行数从 `Measure-Object -Line` 换成显式过滤空白行后，四项指标与基线
  逐项一致 → 等价替换，不是口径漂移。
- **CI 门禁有牙齿**（两个负向探针，均退出 1 并点名）：
  - gofmt：工作区放一个格式漂移文件 → `-AllFiles` 模式点名该文件；
  - R10：临时分支提交 `_ = v` → `-Base main` 模式点名 `file:line`，且报告「1 个改动文件」
    证明 base 差分确实枚举到了已提交的改动（不是空转）。
- **棘轮有牙齿**：2026-10-15 的 `-Update` 记录——`chatCompletions` 从 871 行拆到 128 行后
  `oversizeFuncs` 由 8 抬到 7（数字变小才是抬棘轮）。

## 参考

- 同源项目 magpie 的 Seen 计数制（同类坑第 2 次即上规则）与本仓 `.harness/LESSONS.md`
- `docs/superpowers/plans/2026-10-15-engineering-depth-hardening.md`（Task 1–6 的实现计划）
- `docs/superpowers/plans/HANDOFF-2026-10-16-depth-next.md`（本轮待办与决策点）
