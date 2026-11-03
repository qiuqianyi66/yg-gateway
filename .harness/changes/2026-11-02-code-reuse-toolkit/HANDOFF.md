# 交接：代码复用体系（2026-11-02 会话结束状态）

> 新会话第一件事读这份 + `AGENTS.md`，再跑一次门禁确认基线。

## 一、这轮做完什么

| # | 目标 | 状态 | 证据 |
|---|---|---|---|
| ① | `internal/pkg` 原语层 | ✅ | `timex`（129 行，含 `SleepCtx` 去重）、`jsonsafe`（缺失≠零值）；R8 协议常量单源化到 `internal/upstream/protocol.go` + `TestProtocolConstantsR8` 钉死字面值 |
| ② | 前端组件库 | ✅ | `app.js` **4317 → 1622 非空行（−62%）**，拆出 10 个组件（见下） |
| ③ | `.harness/snippets/` 代码块库 | ✅ | 4 块 + 索引（jsonsafe 取值 / timex 等待与日期 / R8 协议常量 / JSON 响应写出），每块带"什么时候**不**该用" |
| ④ | `.harness/prompts/` 四模板 | ✅ | feat-dev / code-review / debug-triage / perf-optimize |

**门禁**：`scripts/pre-commit.ps1` **11/11 通过**；`go test ./...` 30 包全绿。

## 二、前端组件（index.html 加载顺序即依赖顺序）

```
app_core.js            129  纯函数（esc/ago/dur/fmtTok/csvCell…）
app_views_models.js     97  模型目录表
app_views_packages.js  374  积分包/到期分布
app_views_keys.js      127  Key 弹窗
app_views_accounts.js  374  账号池
app_views_usage.js     493  用量 + 用量明细
app_views_config.js     98  配置页
app_views_tasks.js     276  积分任务 + 任务中心
app_views_logs.js      687  日志 / 请求记录 / Key 管理
app.js                1622  状态 + 路由 + 顶层事件绑定 + 其余视图
app_qr.js              144  QR 编码器（唯一排在 app.js 之后的组件）
```

## 三、三条规则型测试（新增组件自动受约束）

1. `TestComponentLoadOrder` —— `app_core.js` 与所有 `app_views_*.js` 必须排在 `app.js` **之前**。
2. `TestViewsComponentEmbedded` —— 表驱动：函数在组件里、`app.js` 不许留副本（JS 里 `function`
   重复声明**不报错**，只能靠断言）。
3. `TestViewsComponentsAreFunctionOnly` —— **白名单**判据：视图组件只许有 `function/const/let/var/注释`。

## 四、踩过的坑（下一轮别再花时间）

1. **视图组件必须只放声明**。顶层 `$('x').onclick`、顶层 `for` 循环、顶层 `bindRetry($(...))`
   都会在 `app.js` 的 `$` 初始化前执行 → `Cannot access '$' before initialization` → 面板白屏。
   黑名单式检查抓不全（实测漏了 `for` 与 `bindRetry`），必须用白名单。
2. **切片类测试跨文件即失效**。harness 用 `src.indexOf('function A')…indexOf('function B')` 取源码，
   函数分散后切片会跨文件、把顶层语句卷进来（`localStorage is not defined`）。修法已有：
   `funcsOnlyBundlePath`（只含顶层函数）与 `funcsOnlyWithConstsPath`（常量前置 + 函数），
   个别 harness 改成直接执行整份源码。
3. **`-Update` 会连带放宽覆盖率棘轮**。当前 `maxFrontendLines` 4317 → 1622 是大改善，但
   `-Update` 会把 `minCoverage 73.3 → 72.9` 一起写进基线。**先补 `cmd/server` 覆盖率再收紧**。

## 五、未完成

| 项 | 说明 |
|---|---|
| 收紧前端行数棘轮 | 见上，需先补覆盖率 |
| `cmd/server` 覆盖率 73.3→72.9 | 上一轮 `minRequestInterval` 新增分支缺测试（非本轮引入） |
| 11102 三值判定调用分支的端到端测试 | 三值契约有测试，调用路径未覆盖 |
| 代理 fail-open 直连 | 待 owner 决策（配错时反而用共享 IP 出去） |
| `jsonsafe` 全量迁移 | **明确不做**（62 处逐处读过：绝大多数紧跟守卫，已等价 ok 检查）；新代码必用、存量改到哪迁哪 |

## 六、工作区状态（2026-11-03 已拆完，干净）

**两个 feature 已拆成两个提交，工作区 `脏文件=0`**：

| 提交 | feature | 文件数 |
|---|---|---|
| `4fb5639` | `feat(upstream): per-account proxy + 进程级 Session-ID + 11102 三值白名单`（上一轮 ban-risk） | 30 |
| `48cb585` | `refactor: 代码复用体系（pkg 原语 + 前端组件库 + 代码块库 + 提示词模板）`（本任务） | 72 |

### 拆分要点（下次拆提交照这个来）

- **4 个文件混装两个 feature，必须按 hunk 拆**：`AGENTS.md`、`internal/upstream/client.go`、
  `internal/upstream/headers.go`、`internal/upstream/models_fetch.go`。
  hunk 边界是干净的（A 侧 = proxy / session / `doJSON(req,a)` 签名；B 侧 = R8 常量换源 +
  文档数字），**没有"同一 hunk 里两件事"**，所以能拆。
- **送 hunk 子集时补丁必须 LF + 无 BOM**：用 `git apply --cached`，补丁由
  `[System.IO.File]::WriteAllText(..., UTF8Encoding($false))` 写盘。
  PowerShell `Out-File -Encoding utf8` 会带 CRLF → 直接 `patch does not apply`。
- **两个提交各自单独验过绿**：A 单独检出（stash 掉 B）跑 `go build/vet/gofmt -l` + `go test ./...`
  + `check-boundaries` + `check-depth` 全过；B 跑 `pre-commit.ps1` **11/11**。
  所以按提交号追溯部署不会拿到编译不过的快照。
- 拆分时漏过两个未跟踪文件（`internal/upstream/transport_proxy_test.go`、
  `scripts/yg-gateway-watch-hidden.vbs`），已 `--amend` 补进 A。**教训：`git status` 输出被
  `Select-Object -First N` 截断时，未跟踪文件最容易漏**。
- `deploy-prod.ps1 -DryRun` 已确认 `脏文件=0`，`-Build` 不再被脏树拒绝。

### 部署前必读

工作区里现存的 `yg-gateway.exe` 与生产在跑的 sha256 **相同**（`3DE689C1…`），
即那是**旧二进制**。要发这两个提交必须 `deploy-prod.ps1 -Build`（干净树重建），
直接 `-Exe yg-gateway.exe` 等于没换版。

## 七、验证命令

```powershell
go build ./... ; go vet ./... ; gofmt -l ./cmd ./internal
go test ./...                                   # 期望 30 包：25 ok + 5 no test
powershell -NoProfile -File scripts/pre-commit.ps1   # 期望 11/11
```
