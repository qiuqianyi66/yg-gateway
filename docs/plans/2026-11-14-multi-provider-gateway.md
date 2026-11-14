# PLAN — 多 Provider 账号网关：从单厂商养号池到可安装软件（2026-11-14）

> 方法：plan-control 门控（门 1 需求分析 → 批准后进编码）。
> 证据基线：本会话实测源码（行号见各 F）+ magpie/taste-skill 外部对标
> （`docs/reference/similar-projects-learnings-2026-10-25.md`、ADR-026）。
> P0 已落地：`internal/provider`（已完成，本计划不再重复）。

---

## 0. 全流程骨架（先看这张图，再看功能点）

```
                    ┌─────────────────────────────────────────────┐
                    │  P0  Provider 注册表（✅ 已完成，零回归）      │
                    │      Kind / QuotaKind / Descriptor / Registry │
                    └───────────────────┬─────────────────────────┘
                                        │ 只描述静态形状
        ┌───────────────────────────────┼───────────────────────────────┐
        ▼                               ▼                               ▼
┌───────────────┐             ┌─────────────────┐             ┌─────────────────┐
│ P1 凭证层      │             │ P1 额度语义层    │             │ P1 选号策略层    │
│ auth.Provider │             │ quota.Metered   │             │ router.Order    │
│ auth.APIKey   │             │ （Credits 不动）  │             │ /Rotate/Usage   │
└───────┬───────┘             └────────┬────────┘             └────────┬────────┘
        └──────────────┬───────────────┴───────────────────────────────┘
                       ▼
              ┌─────────────────────┐
              │ P1 Ark adapter       │  upstream/ark.go（OpenAI 兼容 + Bearer）
              │ + config.providers[] │
              └──────────┬──────────┘
                         ▼
              ┌─────────────────────┐
              │ P1 池接线（承重墙）  │  非 credits 账号入池：跳过 floor/tier/到期
              │ pool.pick 按 provider 分派 │
              └──────────┬──────────┘
                         ▼
              ┌─────────────────────┐
              │ P2 前端 provider 视图 │  /status + /overview + 面板
              └──────────┬──────────┘
                         ▼
              ┌─────────────────────┐
              │ P3 交付形态           │  绿色 zip + 安装脚本 + 托盘（Go）
              └──────────┬──────────┘
                         ▼
              ┌─────────────────────┐
              │ P4 豆包 C 端（阻塞）   │  3 条 unknown 未消解，仅登记不做
              └─────────────────────┘
```

**核心判断（先摆明）**：这不是"把 pool 重写成 magpie"。magpie 管的是"凭证转发 + 额度窗口 +
路由"，YG 管的是"账号生命周期运营"（17 个养号任务、积分到期、成本实测）。本计划只做**加法**：
把"非 CodeBuddy 账号"接进同一套健康状态机（冷却/熔断/在途/连败降权），额度语义与选号策略按
provider 分派。**现有 CodeBuddy 路径行为逐位不变**是硬验收。

---

## 1. 功能点（F 编号，每条 ≤4h）

### F1 · Provider 注册表（✅ P0 已完成，仅登记）

- 落点：`internal/provider/provider.go`（Kind / QuotaKind / Descriptor / Registry，叶子包）
- 验收：`go test ./internal/provider` ok；`check-boundaries` 通过；allow 表 `provider = @()`。
- 状态：**已完成并在本会话跑过门禁**（31 包全绿，覆盖率棘轮 74%→74.7%）。

### F2 · 凭证层扩展：`auth.Auth` 加 Provider / APIKey

**方案**
- `internal/auth/auth.go:22-62` 的 `Auth` struct 加两个字段：
  - `Provider string`：空值语义 = `"codebuddy"`（由 `ProviderID()` 方法归一，字段本身保持空以兼容旧文件）
  - `APIKey string`：仅 `KindOpenAICompat / KindArk` 使用；**永不进日志/`/status`/面板**（R1/R2）
- `Parse`（`auth.go:257-320`）：嵌套形读顶层 `provider`/`api_key`，扁平形同键；缺省不报错。
- `SaveAtomic` 写回时保留原字段（refresh 只改 token 三件套，不动 provider/api_key）。
- 承重墙：**不改 `Lock/Unlock` 与 refresh 写回语义**（`auth.go:78-116`）。

**GWT 验收**
1. Given 旧 auth 文件（无 provider 键）；When Parse；Then `ProviderID()=="codebuddy"`，现有行为逐位不变。
2. Given 新文件 `{"provider":"ark","api_key":"sk-..."}`；When Parse；Then ProviderID()=="ark"，APIKey 可读。
3. Given 任意日志/`/status`/面板输出；When 账号含 APIKey；Then 明文零出现。
4. **牙齿**：删掉 `ProviderID()` 的空值回落（还原 bug）→ 测试 1 FAIL；把 APIKey 塞进 Status → 测试 3 FAIL。

### F3 · 额度语义层：`internal/quota`（只做 Metered，Credits 不动）

**方案**
- 新包 `internal/quota`（叶子，零 internal 依赖）：
  - `State` 接口：`Kind() provider.QuotaKind`、`Remaining() (value int64, unknown bool)`、`Exhausted() bool`
  - `MeteredState`：`Balance int64`（-1 = unknown）+ `FreeRemaining`；**unknown 不是 0**
  - `WindowState`：预留（P2 订阅接入，本批不实现）
- **不搬家 credits**：CodeBuddy 的积分/到期/tier 逻辑留在 `pool.entry`，避免 quota→pool 反向依赖。

**GWT 验收**
1. Given 余额未配置；When `Remaining()`；Then `(0, true)`（unknown），不是 `(0,false)`。
2. Given 余额 100、免费额度 50；When Remaining；Then 150 且 unknown=false。
3. Given 余额 0；When Exhausted；Then true。
4. **牙齿**：把 unknown 映射成 `false`（还原 bug）→ 测试 1 FAIL。

### F4 · 选号策略层：`internal/router`

**方案**
- 新包 `internal/router`（依赖 provider，不依赖 pool）：
  - `Mode` 枚举：`credits / order / rotate / usage / pace`
  - `View` 结构（由 pool 装配期填充，**router 不读 pool**）：`Healthy bool`、`QuotaValue int64`、
    `QuotaUnknown bool`、`InFlight int`、`LastUsed time.Time`、`Weight float64`
  - `Strategy` 接口：`Mode()`、`Pick(views []View) (index int, ok bool)`
  - 实现：`Order`（首个可用）、`Rotate`（游标轮转）、`Usage`（最少使用优先，并列按 LastUsed）
  - `credits` 模式**不实现**：直接委托 `pool.pick` 现有权重（`pick.go:70-161`），避免双实现漂移。
- `pace` 留 P2（需要窗口重置时间，属订阅语义）。

**GWT 验收**
1. Given 3 个可用账号按 order；When 连续 Pick 且首个失败退出候选；Then 返回下一个。
2. Given rotate；When 连续 Pick 3 次；Then 依次返回 0,1,2,0（游标确定性）。
3. Given usage；When 账号 inFlight 不同；Then 返回 inFlight 最小者。
4. **牙齿**：order 在首个不可用时仍返回它（还原 bug）→ 测试 1 FAIL。

### F5 · Ark adapter + `config.providers[]`

**方案**
- `internal/upstream/ark.go`（新建，R6 合规）：
  - 复用 `OpenAICompatEndpoint`（`openai_compat.go:20-60`）的 URL/Bearer/Accept 三件事
  - `ArkEndpoint` 字段：`BaseURL`、`APIKey`、`Models`；`ChatCompletionsURL()` = base + `/chat/completions`
  - **不新建 HTTP 客户端**：走 `http.DefaultClient` 同现有模板
- 配置（`cmd/server/config.go`）：
  - 新增 `providers []ProviderConfig`：`{id, kind, quota, base_url, api_key, models[], enabled}`
  - normalize：`kind` 非法 → 该条不启用（记日志）；`enabled` 缺省 false
  - 模型路由：请求 `model` 命中某 provider 的 `models[]` → 该 provider；未命中 → CodeBuddy（零回归）
- **unknown（显式登记）**：Ark 免费额度/余额查询端点未验证 → F3 的 Metered 值先来自静态配置
  `balance`，查询接口留 P2；不得写成"已支持额度查询"。

**GWT 验收**
1. Given providers 配置 ark + `models:["doubao-seed-2-1-pro"]`；When 请求该 model；Then 出站到 ark base，
   带 `Authorization: Bearer <api_key>`。
2. Given 未配置任何 provider；When 任意请求；Then 走 CodeBuddy，行为零回归。
3. Given provider 的 kind 非法；When 启动；Then 该条不启用，其余正常（不 panic）。
4. **牙齿**：去掉 Bearer header（还原 bug）→ mock server 断言 FAIL；把未命中模型路由到 ark（还原 bug）→ 测试 2 FAIL。

### F6 · 池接线（承重墙，只加分支）

**方案**
- `pool.entry`（`entry.go:219`）加 `provider string` + `quotaKind provider.QuotaKind`（创建时从 Auth 快照）
- `pool.pick`（`pick.go:70`）入口分派：
  - `credits`（CodeBuddy）→ **原逻辑一行不改**
  - `metered` / `window` → 跳过 `floorBlockedForModel`（`pick.go:397`）、`modelCost` tier 探索、
    `expiringNow` 到期加成；走 `router.Strategy`（order/rotate/usage）
- 共享不变：`inFlightFull`（`pick.go:507`）、熔断、软冷却、连败降权、`SetGlobalMaxInFlight`
- `state.json`（`entry.go:608`）加 `provider` 字段，缺省按 codebuddy；**版本闸门不升**（ADR-009）

**GWT 验收**
1. Given 池内 2 个 codebuddy + 2 个 ark；When 请求 ark 模型；Then 只选 ark 账号，且同账号并发受
   `inFlightFull` 约束（与 codebuddy 账号共享全局上限）。
2. Given 仅 codebuddy 账号；When 任意请求；Then 选号结果与改动前逐位一致（回归快照测试）。
3. Given ark 账号余额 0（Exhausted）；When 请求；Then 不选它（跳过），且 `/status` 显示冷却原因。
4. **牙齿**：让 metered 账号走 `floorBlocked`（还原 bug）→ 测试 1 FAIL；改 codebuddy 权重函数（还原 bug）→ 测试 2 FAIL。

### F7 · 前端 / 后端 provider 视图

**方案**
- `/status`（`internal/server`）：`accounts[].provider`（缺失省略 = unknown）+ 顶层 `providers[]`（注册表）
- `/overview`（`panel.go:267`）：加 `providers` 键；账号卡 `provider` 透传
- 前端（`app_views_accounts.js:110` 账号卡）：
  - provider 徽标：`codebuddy` 绿、`ark` 蓝；**字段缺失不渲染**（不是"不支持"）
  - 账号池页顶部加 provider 汇总条：`{id, kind, quota, 账号数, 启用}`；空注册表显示"仅 CodeBuddy"
- 样式沿用已扁平的令牌（本轮 panel-visual-flat 已去渐变）

**GWT 验收**
1. Given 账号无 provider 字段；When 面板渲染；Then 不显示任何 provider 徽标（unknown ≠ false）。
2. Given 注册表有 ark；When 打开账号池；Then 汇总条显示 ark 及其账号数。
3. **牙齿**：把缺失渲染成"不支持"（还原 bug）→ 前端测试 FAIL。

### F8 · 交付形态：绿色包 + 安装脚本（P3）

**方案（先做零依赖版，托盘后置）**
- `scripts/package-release.ps1`：
  - `go build -trimpath -ldflags="-s -w" -o yg-gateway.exe ./cmd/server`
  - 输出 `dist/yg-gateway-<version>-windows-amd64.zip`：exe + `config.example.json` + `README.md` + `启动.cmd`
  - `启动.cmd`：`yg-gateway.exe -config config.json`，不存在则先复制 example
- 可选（同一脚本 `-Installer` 开关）：调用 Inno Setup `iscc.exe` 生成 `.exe` 安装器
  （检测不到则打印提示并只产出 zip，不硬失败）
- **不做 Electron / WebView 外壳**：会从 ~10MB 变成 100MB+ 并引入运行时，破坏 W6 承重墙。

**GWT 验收**
1. Given 干净构建；When 跑 `package-release.ps1`；Then 产出 zip，解压双击 `启动.cmd` 能起服务。
2. Given 无 Inno Setup；When 加 `-Installer`；Then 只产 zip + 明确提示，退出码 0（不是失败）。
3. **牙齿**：zip 缺 config 模板 → 解压后启动失败（验收 1 FAIL）。

---

## 2. 承重墙声明（默认不改，触及必须显式声明）

| # | 模块 | 为什么是承重墙 | 本计划是否动 |
|---|---|---|---|
| W1 | `pool.pick` 的 credits 权重组合（`pick.go:70-161`） | 养号友好选号是第 2 条定位 | F6 只加 provider 分支，**credits 路径一行不改** |
| W2 | `auth` 凭证锁与 refresh 写回（`auth.go:78-116`） | 并发安全 + R1 | F2 只加字段，不动锁与写回 |
| W3 | `upstream` 的 WorkBuddy 报文（`headers.go`、`client.go:266-268`） | R8 红线 | F5 新建 ark.go，不碰既有 header |
| W4 | `state.json` 格式与 ADR-009 闸门 | 旧程序读新文件会静默丢状态 | F6 只加可选字段，版本号不升 |
| W5 | `panel.api()` 强制鉴权 | 管理端点裸奔是历史事故面 | F7 走 `p.api()` 注册 |
| W6 | 单二进制零依赖（`go.mod` 个位数、无 Chromium/Node） | 第 1 条定位 | 全计划不引入运行时；taste-skill 的 Node 脚本不落地 |

## 3. 红线（零违反）

- **R8**：WorkBuddy 三段式 UA / `wb2api-<ms>` / `chatcmpl-wb2api` 不改名、不参数化
- **R6**：新 provider 出站只落 `internal/upstream`（`ark.go`），server 不持有 HTTP 客户端
- **R1/R2**：APIKey 不进日志/`/status`/面板；`/status` 只显示 `api_key_configured: true`
- **R5**：密钥比较仍走 `internal/httpauth`
- **依赖边界**：新增 `quota` / `router` 包必须登记 allow 表并写理由
- **测试同提交**（L-11）、**提交只含本任务文件**（L-21）

## 4. 粒度、分批与顺序（每批 ≤40%）

| 批 | 功能点 | 估时 | 依赖 |
|---|---|---|---|
| B1 | F2 + F3 | 4h + 3h | F1 ✅ |
| B2 | F4 + F5 | 4h + 3h | F2/F3 |
| B3 | F6 | 4h | B2 |
| B4 | F7 | 4h | B3（需真实 provider 数据，避免空壳 UI） |
| B5 | F8 | 2h | B4 |

- 每批结束跑 `pre-commit.ps1 -Fast` + 相关包测试；B3 后跑全量 `go test ./...`。
- B3 后跑手动回归：`python scripts/yg-regression-test.py --skip S4,S8`。
- 变更记录：每批一个 `.harness/changes/` 目录（REQ 引用本计划 + summary + 回滚）。

## 5. 回滚

- **配置级**：`providers` 置空 = 单 provider；`enabled:false` = 单条下线；无需回滚二进制。
- **代码级**：每批独立 commit，`git revert` 该批即可；无 `state.json` 迁移（W4 不动）。
- **生产级**：`scripts/deploy-prod.ps1`（备份 → 端口释放 → PE 预检 → 替换 → 健康轮询 → 冒烟 →
  失败自动回滚）；退出码 2 = 已自动回滚，4 = 生产可能不可用，不可混读。

## 6. 不做（防范围蔓延；每条写明理由）

- **不重写 `pool` 为 provider-agnostic 通用池**：两套语义（credits vs metered）在同一状态机里共存，
  重写等于用差异化定位换通用性，收益为负。
- **不接豆包 C 端 / 豆包工作**：需 Chromium 注入 `a_bogus/msToken` 或任务式接口适配，破坏 W6；
  3 条 unknown（剩余点位接口、SSE 形状、Ark 额度查询）未消解前只登记不做。
- **不做 Electron / WebView 桌面壳**：见 F8。
- **不把 taste-skill 整包安装**：100+ 条落地页规则与运维面板无关；只吸收 app-screen 禁令
  （已在 `panel-visual-flat` 落地）。
- **不做多租户计费 / 兑换码 / OIDC**：AGENTS.md「不是什么」第 1 条。
- **不做路由组 `pace`**：需要订阅窗口重置时刻，P2 再评估。

## 7. 待确认（本轮需回答，≤5 条）

1. **P1 首家 provider 选谁？** 建议 Ark（OpenAI 兼容最省，且是豆包模型官方正门）；备选 DeepSeek/Kimi
   官方端点（同样 OpenAI 兼容，验证成本更低）。
2. **APIKey 的存放位置？** 建议随 auth 文件（`auths/*.json` 的 `api_key` 键，权限 0600）；备选
   config.providers[]（便于集中管理但进配置文件）。
3. **模型归属判定方式？** 建议显式 `models[]` 白名单（未命中回落 CodeBuddy）；备选前缀 `ark/`。
4. **F7 前端改动范围？** 建议只做"账号卡 provider 徽标 + 顶部汇总条"；备选完整 Providers 管理页（+2 天）。
5. **F8 是否需要 Inno Setup 安装器？** 建议先只在 zip 绿色包（1h）；安装器作为可选开关。
