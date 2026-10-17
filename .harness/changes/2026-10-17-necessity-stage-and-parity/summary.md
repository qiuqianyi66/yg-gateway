# 变更记录 — 阶段 0 必要性入规范 + 前端体量入棘轮 + 沙箱副本机器核对

- 目录/分支：`.harness/changes/2026-10-17-necessity-stage-and-parity/`
- 需求：`docs/prd/REQ-necessity-stage-and-frontend-ratchet-2026-10-17.md`
- 必要性分析：`docs/PLAN-necessity-review-2026-10-17.md`（14 项逐项裁定）
- 日期：2026-10-17
- 分级：**L2**（新增门禁检查项 + 新增棘轮指标）

## 一、改了什么

| # | 文件 | 改动 | 验收 |
|---|---|---|---|
| 1 | `.harness/rules/开发流程规范.md` | 新增「零、阶段 0：必要性分析」：四问（真实问题 / 不做会怎样 / 现状是否已变 / 更便宜手段）+ 四结论（做 / 不做 / 等条件 / 降级做）+ 三条硬约束；十阶段表加阶段 0 行；使用方式加 🔴 条目；完成标准加一条 | 本变更自己按它写（`REQ-...2026-10-17.md` 有「必要性」段） |
| 2 | `scripts/check-req-necessity.ps1` | **新增**：本次新增的 `docs/prd/REQ-*.md` 必须含 `^#{2,6}\s*.*必要性`；本地查 staged 新增 + 未跟踪，CI 查 `-Base` 的新增；**只查新增** | F-1001 |
| 3 | `scripts/pre-commit.ps1` | 新增「检查 8/8」接入上述脚本（`-Base` 透传）；原 1–7 的「/7」编号改「/8」 | F-1002 |
| 4 | `scripts/check-depth.ps1` | 新增度量 5「前端体量」：扫 `internal/**/*.js`（排除 node_modules、口径 = 非空行），输出「最大前端文件行数」，加入棘轮对比键（越大越差） | F-1003 |
| 5 | `.harness/depth-baseline.json` | 新增 `maxFrontendLines: 4378`（其余四项不动） | F-1003 |
| 6 | `internal/pool/sandbox_parity_test.go` | **新增** `TestSandboxFilesInSync`：两份沙箱文件归一化后逐行比对；用 `runtime.Caller` 定位源目录（沙箱已 chdir，不能用相对路径） | F-1004 |
| 7 | `docs/PLAN-necessity-review-2026-10-17.md` | **新增**：14 项必要性裁定（做 3 / 降级 2 / 不做 6 / 等条件 3），含"这次分析省掉了什么"的复算 | 就是它本身 |

## 二、怎么验证的（全部本机实跑）

### 2.1 阶段 0 检查的牙齿

```
# 本轮 REQ 含「## 必要性」→ 通过
$ check-req-necessity.ps1 → exit=0  [OK] 新增需求文档均含「必要性」段（1 个）

# 造一个缺该段的新 REQ → 红
$ check-req-necessity.ps1 → exit=1
  [FAIL] 新增需求文档缺「必要性」段：docs/prd/REQ-teeth-probe-2026-10-17.md
  → 按 …「零、阶段 0」补一段：1) 真实问题 2) 不做会怎样 3) 现状是否已变 4) 更便宜手段

# 探针删除后复跑 → exit=0（不误报存量 7 份历史 REQ）
```

### 2.2 前端体量棘轮的牙齿

```
$ check-depth.ps1（基线 4378）
  最大前端文件行数  : 4378  (internal\panel\app.js)
✅ 深度指标未回退   exit=0

$ 给 app.js 加一行 → check-depth.ps1
❌ 深度指标回退（1 项）：REGRESSED: maxFrontendLines 4378 → 4379    exit=1
（还原后 SHA256 与备份一致）
```

### 2.3 沙箱副本核对的牙齿

```
$ go test ./internal/pool/ -run TestSandboxFilesInSync
--- PASS（归一化后 252 行一致）

$ 往 internal/pool/main_test.go 加一行 var _ = "teeth"
--- FAIL: 两份沙箱文件行数不一致（归一化后 253 vs 252）：改 internal/pool/main_test.go 必须同步改 internal/server/main_test.go
（还原后 SHA256 一致）
```

### 2.4 五项门禁 + 总闸

见 §五 的最终运行记录（含 8 项检查与耗时）。

## 三、必须记录的三个坑

1. **测试不能靠 `os.Getwd` 读兄弟包文件**：第一版 `TestSandboxFilesInSync` 用相对路径
   `../server/main_test.go`，直接报「找不到文件」——因为本包的 `TestMain` 已经把 cwd **chdir
   进沙箱**（这正是隔离生效的证据）。改用 `runtime.Caller(0)` 定位源目录。
   *教训*：沙箱包的测试里，任何路径都必须从源文件位置推导，不能假设 cwd。
2. **前端指标不能塞进 `maxFileLines`**：直觉写法是把 `.js` 一起扫进度量 1，但那会让
   `maxFileLines` 从 2058 跳到 4378 —— 这不是"纳入门禁"，是**放宽 `.go` 的棘轮**（D-3）。
   必须新开指标。
3. **编号改名要一次做全**：给 `pre-commit.ps1` 加检查 8 后，原来 7 处「检查 N/7」的分子分母
   全部要改，否则输出会自相矛盾（`检查 7/8` 后面紧跟 `检查 8/8` 混杂 `/7`）。用一次
   `-replace '检查 (\d)/7','检查 $1/8'` 统一改，再逐行核对。

## 四、决策（含未被采纳的方案）

| # | 决策 | 选择与理由 |
|---|---|---|
| D-1 | 必要性段：机器校验存在 vs 纯人工 | **机器校验存在**（只查小节，不查质量）。纯人工第一轮就会被跳过 |
| D-2 | 只查新增 vs 连存量一起查 | **只查新增**。存量 7 份 REQ 都没有这一节，一起查会让门禁首次运行就红 → 规则立刻被绕过 |
| D-3 | 前端体量：新指标 vs 塞进 `maxFileLines` | **新指标 `maxFrontendLines`**（塞进去等于放宽 `.go` 棘轮） |
| D-4 | 沙箱同步：重构 vs 断言 | **断言**（25 行）。抽公共包被两份文件顶部注释明确拒绝（零新依赖 + 边界白名单 + 23 包口径） |
| D-5 | `app.js` 拆不拆 | **不拆**。无浏览器级 E2E 兜底，风险 > 收益；先纳入度量防增长，拆留在有 E2E 之后 |

## 五、门禁与回滚

- 门禁：`gofmt -l`（空）· `go vet`（空）· `go test ./...`（20 包 ok）· `check-boundaries.ps1` ·
  `check-depth.ps1`（含新指标）· `pre-commit.ps1` 全量 8 项。
- 回滚：
  - 全部：`git revert <本提交>`（纯增量，无数据/协议变更，不影响生产二进制）
  - 只退必要性门禁：删 `scripts/check-req-necessity.ps1` + 去掉 `pre-commit.ps1` 的检查 8/8
    （并把 `/8` 编号改回 `/7`）
  - 只退前端棘轮：从 `check-depth.ps1` 删度量 5 与对比键，从 baseline 删 `maxFrontendLines`
  - 只退沙箱断言：删 `internal/pool/sandbox_parity_test.go`

## 六、遗留（如实记录）

1. **必要性段只校验"有没有"，不校验"写得好不好"**：内容质量仍靠评审。这是刻意的（D-1）。
2. **存量 7 份 REQ 没有必要性段**：不追溯（D-2）。它们的历史判断已由 `PLAN-necessity-review` 补裁定。
3. **`maxFrontendLines` 只覆盖 `internal/**/*.js`**：`index.html` 与 CSS 未纳入（当前都远小于 app.js，
   且 embed 的 CSS 就在 html 里）。重评条件：任一前端文件超过 1000 行。
4. **A6/A7/D2/D3/生产换版仍是"等条件"**：条件已写进 `PLAN-necessity-review-2026-10-17.md` 每项的
   「重评条件」，不要再无条件下重复立项。
