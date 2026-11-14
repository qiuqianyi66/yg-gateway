# 多 Provider 账号网关（multi-provider-accounts）变更摘要

需求：`.harness/changes/multi-provider-accounts/docs/prd/REQ-multi-provider-2026-11-14.md`
ADR：`docs/adr/ADR-026-provider-abstraction.md`
计划：`docs/plans/2026-11-14-multi-provider-gateway.md`（plan-control 门 1 产物，已批准）
分级：L3（架构 / 领域模型）
复杂度：C 耦合（auth / pool / upstream / server / panel，P1 起生效）

## 一、改了什么（P0 + P1，F1–F6 已落地）

| 变更 | 落点 | 性质 |
|---|---|---|
| provider 静态描述 + 注册表 | `internal/provider/provider.go` | 纯新增，叶子包（零 internal 依赖） |
| `Auth.Provider` / `Auth.APIKey` + `ProviderID()` / `APIKeyConfigured()` | `internal/auth/auth.go` | 增字段（空值语义=codebuddy），Parse/SaveAtomic 兼容 |
| quota 状态抽象（State/MeteredState/WindowState） | `internal/quota/quota.go` | 纯新增，不搬 credits |
| router 路由策略（Order/Rotate/Usage + View） | `internal/router/router.go` | 纯新增，零 internal 依赖 |
| Ark OpenAI 兼容端点 | `internal/upstream/ark.go` | 复用 OpenAICompatEndpoint 模板 |
| `config.providers[]` + `normalizeProviders` | `cmd/server/config.go` | fail-fast 校验，credits 被拒 |
| pool 选号 provider 分派 | `internal/pool/entry.go` `pool.go` `pick.go` | entry 加 provider；providerOfModel 注入；pickNonCredits |
| server 装配 + 出站路由 | `internal/server/handler.go` `chatrun.go` `handler_provider.go` | serveProviderCompat 独立路径 |
| provider 装配（路由注入 + 端点表） | `cmd/server/main.go` `provider_wiring.go` | 拆到独立函数控 main.go 体量 |
| allow 表登记 quota/router/pool→router | `scripts/check-boundaries.ps1` | 写理由 |
| 面板视觉去 AI 味（panel-visual-flat） | `internal/panel/index.html` | 删渐变、修靛蓝残留、色值回令牌 |
| Status 加 Provider 字段 + 透传 | `internal/pool/entry.go` `state.go` | ProviderID() 派生，omitempty 缺省=unknown |
| `/status` providers[] + `/overview` providers | `internal/server/status.go` `panel.go` | 注册表元数据 + 账号数 |
| 前端 provider 徽标 + 汇总条 | `app_views_accounts.js` `provider.css` | 非 codebuddy 蓝标；空注册表「仅 CodeBuddy」 |
| 交付打包脚本 | `scripts/package-release.ps1` | zip 绿色包 + 可选 Inno Setup 检测 |

**契约：credits 路径逐位不变。** `Auth.Provider` 空值 = codebuddy，未命中 models[] 白名单 = 回落 credits 选号；credits 候选集加 provider 过滤（防 ark 账号混入加权）。

## 二、怎么验证的（实跑）

| 检查 | 结果 |
|---|---|
| `go test ./...` | 33 包全绿（含新增 quota/router/provider + 改动 auth/pool/server/upstream/cmd-server） |
| `go build ./...` / `go vet ./...` / `gofmt -l ./cmd ./internal` | 通过 / 无输出 / 无输出 |
| `scripts/check-boundaries.ps1` | ✅ 通过（exit 0） |
| `scripts/check-depth.ps1` | ✅ 未回退（minCoverage ≥74.7%，maxFrontendLines 1827 持平） |
| `scripts/pre-commit.ps1 -Fast` | ✅ 全部通过（11/11） |

单测：provider 8 组、auth 5 组、quota/router/config 各若干、pool 6 组（provider 分派 + 牙齿）、server 5 组（出站到 provider 端点 + providerSummary + /status provider 透传）、cmd/server 10 组（config + providerSummaryView）、panel 前端牙齿（provider 徽标缺失不渲染成"不支持"）。provider_pick_test 含牙齿测试（还原 nil 路由 / provider 未派生 bug 必 FAIL）。

> 本机无 gcc，`-race` 未跑（AGENTS.md：由 CI 独占）。

## 三、已完成（F7 / F8）

- [x] F7 前端 provider 视图：`accounts[].provider`（Status 加 Provider 字段，ProviderID() 派生）+ `/status` 顶层 `providers[]` + `/overview` `providers` 键 + 账号卡 provider 徽标（非 codebuddy 蓝标）+ 顶部汇总条（空注册表显示「仅 CodeBuddy」，缺失不渲染=unknown）
- [x] F7 provider CSS 拆到 `provider.css`（panelStyles 清单登记，控 index.html 棘轮）
- [x] F8 打包：`scripts/package-release.ps1`（`go build -trimpath -ldflags="-s -w"` → zip 绿色包：exe + config.example.json + README.md + 启动.cmd；`-Installer` 检测 iscc.exe，无则只产 zip 退出码 0）
- [ ] 手工回归：`python scripts/yg-regression-test.py --skip S4,S8`（待真实 Ark 账号入池后跑）

## 三·五、第二轮打磨（实测发现的真 bug，2026-11-14）

功能全绿后按「找真 bug」口径自检，6 处修复（每处都补测试；第 2 条做了牙齿验证）：

1. **`provider.css` 路由缺失**：`panelStyles` 已登记 `provider.css`，但 `routes.go` 只注册了 `gamify.css` → 页面 404、CSP 下样式全废。补 `providerStyle` handler + 路由 + `provider_css_test.go`（serve 200 / Content-Type / CSP / 嵌入非空 / index.html 无残留）。
2. **`auth.Parse` 强制 accessToken**：Ark 类 provider 用 APIKey 认证、没有 OAuth token，原逻辑一律拒绝 → 用户根本无法把 Ark 账号放进 `auths/`，多 provider 路由形同虚设。改为「非 codebuddy 且 api_key 非空则放行」，`SaveAtomic` 同步（两者都空才拒）。牙齿实测：还原旧逻辑 → `TestParseProviderAPIKeyOnlyAccount` FAIL。
3. **凭证双真相源**：`serveProviderCompat` 只用 `config.providers[].api_key`，忽略账号级 `api_key`。改为账号级优先、provider 级回退（一账号一 key 的常见形态可独立计费）。
4. **provider 账号卡片误显示 codebuddy 专属操作**：签到/余额/任务按钮对 Ark 账号点了必失败；积分为 0 还显示「0」。改为 `isProvider` 只留暂停/禁用/移除，credits 显示「—」、进度条留空。
5. **scheduler 余额刷新无 token 守卫**：`RunBalanceRefresh` 是 7 个调度遍历点中唯一没有 `AccessTokenValue()==""` 守卫的 → 每轮都会拿 Ark 账号打 codebuddy 积分接口（401 + error 日志 + 潜在误改启用态）。已补守卫，与其余 6 点同口径。
6. **`/v1/models` 不含 provider 模型**：配了 Ark 模型但客户端（Cursor/Cline）查不到、等于不可用。新增 `appendProviderModels`（裸名追加、已存在去重、禁用 provider 不列、按 id 字典序稳定）。

文档同步：`config.example.json` 加 `providers` 示例条目（`enabled: false`，零影响）；`README.md` 新增「多 provider 账号网关 `providers`」一节（配置示例、账号放置、凭证优先级、不串池、面板展示）。

门禁：全量 `go test ./...` 全绿（30 包）、`pre-commit.ps1 -Fast` 11/11（含 gofmt 漂移修复后复跑）。

## 四、怎么回滚

- 删 `internal/provider/` `internal/quota/` `internal/router/` + allow 表对应行 + config `providers[]` 字段，credits 路径零影响；
- `Auth.Provider` 空值回退 codebuddy，不涉及 `state.json` 迁移（provider 不持久化，auth 文件是真相源）。

## 五、Unknown（未消解，不得写成否定）

1. 豆包工作 / 豆包 C 端是否有可查「剩余点位」的接口 —— requires：带登录态抓包一次。
2. 豆包工作接口能否映射为 chat completions 流 —— requires：抓一次真实任务请求看 SSE 事件形状。
3. Ark 免费额度查询端点与口径 —— requires：实测 `/api/v3` usage/billing。

## 六、边界声明（评审必看）

- `Descriptor` 只答静态形状，**不得**加运行态字段（剩余额度 / 健康 / 冷却）；
- 运行态（额度/健康/冷却）留在 pool，`quota.State` 只做读数接口，不搬家 credits；
- WorkBuddy 三段式 UA / `wb2api-<ms>` conversationId / `chatcmpl-wb2api` 哨兵留在 codebuddy 协议族内，不做参数化（R8）；
- 非 codebuddy 出站走独立 `serveProviderCompat`，不复用 serveStream/serveNonStream（其 SSE 解析/积分统计是 WorkBuddy 专属）。
