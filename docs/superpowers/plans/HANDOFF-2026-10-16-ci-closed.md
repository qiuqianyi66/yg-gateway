# HANDOFF — CI 治理闭环已收口：本轮结果与下一批任务

> 生成于 **2026-10-16**（本轮收尾），HEAD = **`0529730`**。
> **本文取代** `docs/superpowers/plans/HANDOFF-2026-10-16-depth-next.md`
> （那份的 T1–T10 已全部收口，其 §8 保留了当时的执行结果与三处对原文判断的修正，仍有阅读价值）。
> 再往前：`2026-10-15-engineering-depth-hardening.md`（Task 1–6）与 `HANDOFF-depth-governance.md`（已过时）。

---

## 0. 开工前 30 秒自检（照做）

```powershell
# 环境：Windows PowerShell 5.1 —— 本机**没有**独立 pwsh；本机无 gcc，-race 只能靠 CI
$go = "C:\Program Files\Go\bin\go.exe"
$env:GOPROXY='https://goproxy.cn,direct'; $env:CGO_ENABLED='0'

& $go build ./... ; & $go vet ./... ; & gofmt -l ./cmd ./internal   # gofmt 必须无输出
& $go test ./... -count=1                                          # 23 包：20 ok + 3 no test files
powershell -NoProfile -File scripts/check-boundaries.ps1            # 退出码 0
powershell -NoProfile -File scripts/check-depth.ps1                 # 退出码 0（棘轮 minCoverage 71.9%）
powershell -NoProfile -File scripts/pre-commit.ps1                  # 六项总闸（含 PS 引号陷阱）
```

**禁令**：不许 `git add -A`；不许放宽 `check-depth` 阈值；不许用「重跑至绿」代替修 flake；
跑覆盖率 profile 一律写 `$env:TEMP`（见 `.harness/LESSONS.md` L-3）；**提交信息写文件 + `git commit -F`**
（L-1 第 3 次踩坑的结论）。

---

## 1. 当前状态（已实测，别重新怀疑）

### 1.1 CI：两条发布线都绿，且都过同一道门禁

| workflow | 状态 | 说明 |
|---|---|---|
| `go-binaries` | ✅ 绿 | `test`（go test/vet/node --check/govulncheck）+ `gate` + `race` + 五平台 `build`；`publish` 仅 tag 触发 |
| `docker-ghcr` | ✅ 绿 | `gate` → `build-and-publish`（多架构镜像推 ghcr） |

- **门禁已抽成可复用 workflow** `.github/workflows/gate.yml`（`workflow_call`），
  两条线**共用同一份实现**；两条线的 `build` 都 `needs: gate`（门禁不过就不产出二进制/镜像）。
  **CI 运行图实证**（run `0529730`）：docker-ghcr 的 `gate` 11:55:12 起 → `build-and-publish`
  **11:56:46** 才起（严格等 gate 绿）；go-binaries 的 `test`/`gate`/`race` 11:55:11 起 →
  五平台 `build` 11:56:49 起。即「镜像线绕过治理」这条路已经不存在了。
- 首跑暴露的三个真问题已全部修掉：① `internal/upstream` 的 **data race**（测试写法，已改走真实
  `httptest.Server`）② `panel` 测试依赖未提交的生产改动（已把那份在途工作提交）③ `panel.go` 的 gofmt 漂移。

### 1.2 生产实例

- `D:\yg-gateway`（127.0.0.1:7863）跑 **T7 版本**：exe SHA256 前缀 `4EE83B41102E0846…`；
  备份 `yg-gateway.exe.pre-t7-unknown-20261016.bak`（旧版前缀 `109E67E060C49E87…`）。
- 部署冒烟已过：`/healthz`、`/v1/models`（20 个）、`/panel/` 200、非流式一发、流式一发（`STREAM_OK` + `[DONE]`）。
- **换版纪律**（本轮新学，见 L-9）：先轮询端口释放再启动；启动要 `Start-Process -RedirectStandardError`
  落新实例日志；健康轮询 + 失败自动回滚（回滚用**另一个**日志文件，否则会覆盖失败现场）。

### 1.3 文档与治理资产（都在库里，可检索）

| 位置 | 内容 |
|---|---|
| `.harness/LESSONS.md` | **11 条坑 + 3 条 flake**，Seen 计数制（同类第 2 次即上规则/机器检查）。本轮新增 L-9/L-10/L-11 |
| `.harness/changes/2026-10-16-*/` | 6 份变更记录：`ci-gate`、`depth-tasks-4-6`（含补写的 review.md）、`t3-mutation`、`t4-r8-golden`、`t5-sandbox`、`t7-unknown-admission`、`dsh-default-input` |
| `docs/adr/ADR-008-*.md` | 深度门禁选型（棘轮 + 自建 PS 脚本，不引入 linter/覆盖率工具），含两处「自建脚本」的学费 |
| `docs/prd/REQ-usage-unknown-admission-2026-10-16.md` | unknown 不得冒充 0（P1–P4 全实施） |
| `.harness/wiki/cc-switch-deep-read.md` | cc-switch 深读（170 行 / 60 处 file:line / observation-inference-unknown 标注 + 对照本仓 14 行） |
| `docs/superpowers/plans/HANDOFF-2026-10-16-depth-next.md` §8 | T1–T10 逐条结果 + 三处对原文判断的修正（T5 因果、T8 取材、T9 验证方法） |

### 1.4 文件卫生：这些是**并行会话**的（别碰）

| 文件 | 状态 | 备注 |
|---|---|---|
| `HANDOFF.md`（仓库根） | 已改未提交 | 里面有 **21 包** 等旧计数（实际 23 包），需要它的会话更新；与 `docs/superpowers/plans/` 下的交接文档**是两回事** |
| `README.md` | 已改未提交 | 排版重写 |
| `.harness/rules/COST-CONTROL.md` | **未跟踪** | 是否入库由其决定 |
| `internal/panel/{app.js,panel.go,frontend_ux_test.go}` | **已由本会话代提交**（`043f34f`） | 原因见 §6；内容一字未改，他们的工作区不受影响 |

---

## 2. 硬约束（不可违反）

见 `AGENTS.md`（顶层硬约束）：零新功能 / 零新依赖（`go.mod` 直接依赖仍只有 redis）、R8（协议报文不改名）、
R10（错误不静默吞）、依赖方向、棘轮只许收紧、提交按显式路径、每个 Task 一条提交且 `go test ./...` 全绿。
另有 `.harness/rules/CODE-STANDARDS-CHECKLIST.md`（第九节：L2/L3 改动需 `review.md`）。

---

## 3. 下一批任务（按优先级；每条含 为什么 / 怎么做 / 验收）

### N1 — 把「换版流程」固化成脚本（★高，防再犯）

**为什么**：本轮第一次换版把生产打断了约 1 分钟（kill 后只等 2 秒 → 端口没释放 → 新实例没起来 → 自动回滚），
而且 `start.bat` 的 `start ... >> data\stdout.log` 根本不落子进程输出，失败时**无日志可查**（L-9）。
这些坑写在文档里没用——下次仍会凭记忆写。

**怎么做**：新增 `scripts/deploy-prod.ps1`：`-Exe <新 exe>`（默认从构建产物取）→ 备份（R15，命名
`yg-gateway.exe.pre-<feature>-<date>.bak`）→ **轮询端口释放** → 替换 → `Start-Process` 启动并
`-RedirectStandardError $env:TEMP\...` → 健康轮询 `/healthz`（超时阈值可参）→ 失败自动回滚（回滚用独立日志）
→ 冒烟（`/v1/models` + 流式/非流式各一发）。同一条命令内完成「停/换/启」。

**验收**：故意用**坏的 exe**（比如改坏配置的副本）跑一次 → 脚本自动回滚且服务仍健康；
用真 exe 跑一次 → 冒烟四条全过。**风险**：脚本会短暂重启生产（当前会话自身的模型调用也走这个网关），
建议在低峰执行。

### N2 — `L-3` 的机器检查（★中高，Seen 2 → 按机制必须落地）

**为什么**：`-coverprofile=$out` 未加引号/加单引号 → PS 不展开变量 → 仓库根出现字面名垃圾文件
（`$out`/`$p`/`$tmp`），而执行者以为产出了 profile。已踩 2 次（本轮 T5 子代理又踩一次）。

**怎么做**：在 `scripts/pre-commit.ps1` 加一项：扫描**仓库根未跟踪文件**，名字以 `$` 开头（或形如
`^\.?(out|p|tmp|cov)$` 这种明显是变量名残留）即失败并提示正确写法
（`$out = Join-Path $env:TEMP "cov.out"; go test "-coverprofile=$out"`）。

**验收**：造一个仓库根的字面名文件 → 门禁红并点名；删除后绿。**风险**：误报——只在「未跟踪 + 仓库根」
范围里判，避免误伤 `data/` 下的正常文件。

### N3 — `T5` 的结构性根治：`dynamicModelsCache` 搬进 Handler（★中高）

**为什么**：`internal/server` 的 `-count=3` 红的真因是**进程内包级全局** `dynamicModelsCache` 的 5min 负缓存，
`TestMain` 沙箱隔离不了它；现在靠「用例首行调 `resetDynamicModelsCache()`」，**新增用例忘记调就会复发**。

**怎么做**：把该缓存移入 `Handler` 实例字段（或注入一个带 TTL 的小结构体），去掉包级可变全局
（编码规范第九节要求包级可变全局必须注释说明并给替代方案）。

**验收**：`go test ./internal/server/ -count=3` 全绿**且**测试里不再需要 `resetDynamicModelsCache()`
（grep 该函数应只剩定义处）；新增用例天然隔离（写一条故意不 reset 的用例证明不会互相污染）。

### N4 — R8 golden 的成本：`RunNightChats` 的 4s sleep（★中）

**为什么**：T4 的 golden 覆盖了 `wb2api-night-*`（此前无人断言），代价是要真跑 `RunNightChats`，
其**不可注入的 4s sleep** 让 `internal/upstream` 单轮从 7s 变 11s，每次 `go test ./...` 都付。

**怎么做**：照 T5 给 pool 的办法，把该 sleep 变成可注入（生产默认不变，测试注入 0）。

**验收**：`go test ./internal/upstream/` 单轮回到 ~7s；golden 仍能捕获 `wb2api-night-` 改名（用一个反向变异证明）。

### N5 — 端到端验证两处「只在单测里验过」的行为（★中）

- **T7 的 reqlog 新字段**：`cache_miss_unknown` 至今**没在产线日志出现过**（没有触发 unknown 情形）。
  做法：构造一次「上游没给 miss 且 prompt 不可用」的响应（最省事是写一个一次性假上游 / 或临时改一处），
  确认 JSONL 里出现 `cache_miss_unknown: true` 且**没有** `cache_miss_tokens` 数字。
- **P4 的 `cmd/login` 4xx 分档**：用一个被拒的账号跑 `login poll`，确认输出是「登录被上游拒绝（HTTP 401）…
  继续等待不会成功」而不是「登录未完成」。
- **面板 unknown 展示**：T7 只保证 unknown 进登记链路；现在 panel WIP 已入库，可实际打开面板看 unknown 区块。

**验收**：三条各留一条原始证据（JSONL 行 / CLI 输出 / 面板截图或 DOM 文本）落进对应 change 目录。

### N6 — `T8` 深读里那 4 条高价值建议（★中，需先立 PRD/ADR）

**为什么**：`cc-switch` 深读（`.harness/wiki/cc-switch-deep-read.md`）挑出 4 条下一阶段重构：
退避与额度解耦、快过期积分的单一纯函数、流式 error 门禁、写回前回读校验。**一条都没实现**。

**怎么做**：按本仓纪律先写 `docs/prd/REQ-*.md`（Given-When-Then）+ 必要时 ADR，再动手；一次一条、各自独立提交。

### N7 — 补齐变异证据的边界（★低，但别忘）

`T3` 只独立核验了 `upstream`/`panel`；`auth`/`session`/`scheduler`/`cmd-server` 的「有牙齿」仍是**自述**。
建议按 T3 的配方（`%TEMP%` 副本 + `go env GOMOD` 断言 + 哈希闭环 + 每包 5 条语义变异）补一轮。

### N8 — 两条发布线是否合并（★低，等你拍板）

现状：`go-binaries` 与 `docker-ghcr` 各自调用 `gate.yml` → push 时 gate 跑两次（重复约 2.5 分钟，
公开仓库分钟数免费）。要消除重复，正确做法是合并成一个 workflow（一个 gate job + 两个 build job），
**不是**把 gate 内联回去。

---

## 4. 本轮踩过的坑（已全部进 LESSONS.md，这里只给索引）

| # | 一句话 | 危害 |
|---|---|---|
| L-9 | 换二进制要**轮询端口释放**；`start.bat` 的重定向不落子进程输出 | 生产中断 1 分钟 + 失败时无日志可查 |
| L-10 | 改文件要用**定点编辑**，锚点选「你要保留的那一行」；手写文件一律 LF | 三次「文件还在、内容没了」（覆盖 §四、覆盖 §二 标题、CRLF 导致整篇 diff） |
| L-11 | 测试必须和它依赖的生产改动**同一个提交**（否则 HEAD 自红、本地永远绿） | clone 下来就跑不过，且极易误判成「CI 环境有问题」 |

**自查手段**（L-11 的落地）：提交前用干净 HEAD 的独立工作树复跑一次——
`git worktree add "$env:TEMP\ci-check" HEAD --detach` → 进去 `go test ./... -count=1` → `git worktree remove`。

---

## 5. 明确不做（除非用户改主意）

- 不引入 linter / 覆盖率服务 / 测试框架（零新依赖是定位，取舍已写进 ADR-008）
- 不装 `.git/hooks/pre-commit`（多会话并行会互相卡）
- 不擅自改「留待产品定夺」的计量口径（当前清单已清空，新的先立 PRD）
- 不碰并行会话的文件（`HANDOFF.md` / `README.md` / `.harness/rules/COST-CONTROL.md`）

---

## 6. 一句话交接：本会话替并行会话提交过什么

`043f34f` 提交了并行会话工作区里的 `internal/panel/{app.js,panel.go,frontend_ux_test.go}`（+129/-7）。
**原因**：CI 首跑暴露 HEAD 自红——`coverage_handlers_test.go:1182` 断言的 `cost_ppm`/`cost_samples`
由尚未提交的 `panel.go` 产出；不提交这份在途工作，CI 永远红。
**边界**：内容一字未改（提交前记录了三个文件的 SHA256），他们的工作文件仍在原地；如认为不该提交，
`git revert 043f34f` 即可退回（CI 会随之恢复红）。

---

## 7. 需要用户拍板的决策点

| # | 决策 | 备选 |
|---|---|---|
| D1 | 已泄露的 GitHub token（曾贴进对话） | **建议 revoke 后重新生成**，新值替换到 `GH_TOKEN`（User 级已配好，重启 dsh 生效） |
| D2 | 两条发布线是否合并（消除 gate 重复 2.5 min） | 合并成一个 workflow / 保持两条（各跑一次 gate） |
| D3 | `scripts/deploy-prod.ps1`（N1）是否现在做 | 做（换版有脚本兜底）/ 暂缓 |
| D4 | `L-7`（反斜杠字面量路径）机器检查 | 现在就上（Seen 1，属提前） / 等第 2 次（按机制） |
| D5 | 仓库根 `HANDOFF.md` 的旧计数（21 包） | 由并行会话更新 / 授权本线代改 / 不管 |
