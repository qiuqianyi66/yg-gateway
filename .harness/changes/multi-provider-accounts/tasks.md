# 任务清单：多 Provider 账号网关

> 状态图例：`[x]` 完成并已跑门禁 · `[ ]` 待做 · `[-]` 明确不做（写明理由）

## P0 · 抽象脊柱（零回归）✅

- [x] 建 `internal/provider` 叶子包：Kind / QuotaKind / Descriptor / Registry
- [x] Registry 并发安全（RWMutex）+ Models 切片深拷贝 + List 按 ID 稳定排序
- [x] `scripts/check-boundaries.ps1` allow 表登记 `provider = @()`（叶子，零依赖）
- [x] 单测 8 组 + 门禁全绿

## P1 · 第二个 provider 真正入池（F2–F6）✅

- [x] F2：`Auth.Provider` / `Auth.APIKey` + `ProviderID()` / `APIKeyConfigured()` + Parse/SaveAtomic 兼容
- [x] F3：`internal/quota`（State/MeteredState/WindowState，不搬 credits）
- [x] F4：`internal/router`（Order/Rotate/Usage + View，零 internal 依赖）
- [x] F5：`upstream/ark.go` + `config.providers[]` + `normalizeProviders`（fail-fast）
- [x] F6：pool 选号 provider 分派（providerOfModel + pickNonCredits + credits 过滤）
- [x] F6：server 装配 + 出站路由（serveProviderCompat 独立路径，不复用 serveStream）
- [x] allow 表登记 quota / router / pool→router
- [x] 单测 + 牙齿测试（nil 路由 / provider 未派生 / ark 混入 credits 必 FAIL）
- [x] 门禁：`go test ./...` 33 包 + `pre-commit.ps1 -Fast` 11/11 全过
- [x] ADR-026 记账

## P2 · 面板与打包（F7–F8）✅

- [x] F7 前端 provider 视图：`/status` 加 `accounts[].provider` + 顶层 `providers[]`，`/overview` 加 providers，账号卡 provider 徽标（非 codebuddy 蓝标）+ 顶部汇总条（缺省不渲染，unknown≠false）
- [x] F7 provider CSS 拆到 `provider.css`（控 index.html 棘轮，同 gamify.css 先例）
- [x] F8 打包：`scripts/package-release.ps1`（zip 绿色包 + 可选 Inno Setup 检测）
- [ ] 手工回归：`python scripts/yg-regression-test.py --skip S4,S8`（待真实 Ark 账号入池后跑）

## 打磨轮（真 bug 清理，2026-11-14）✅

- [x] `provider.css` 路由缺失（sitemap 登记但未注册）→ 补 `providerStyle` + 路由 + 测试
- [x] `auth.Parse` 强制 accessToken → Ark 账号进不了池 → 非 codebuddy + api_key 非空放行
- [x] 凭证双真相源 → 账号级 api_key 覆盖 provider 级
- [x] 前端 provider 卡误显示 codebuddy 专属按钮 / credits 0 → `isProvider` 分支
- [x] scheduler 余额刷新无 token 守卫 → 补 `AccessTokenValue()==""` 跳过
- [x] `/v1/models` 不含 provider 模型 → `appendProviderModels` 追加
- [x] `config.example.json` + `README.md` 补多 provider 用法
- [x] 全量 test 全绿 + pre-commit -Fast 11/11 + 牙齿实测（还原 Parse 旧逻辑 FAIL）

## 不做（写明理由）

- [-] 豆包 C 端 / 豆包工作入池：需 Chromium 注入 a_bogus/msToken 或任务式接口适配，破坏「单二进制零依赖」。先消解 REQ §六 的 3 个 unknown。
- [-] 多租户计费 / 兑换码 / OIDC：AGENTS.md「不是什么」第 1 条，本需求不松动。
- [-] 把 WorkBuddy 报文串做成可配置模板：R8 红线（报文内容不得改名）。
- [-] 在 `Descriptor` 里放运行态字段：ADR-026 防漂移边界。
- [-] Electron/WebView 桌面壳：破坏「单二进制零依赖」，改走 zip 绿色包 + 可选 Inno Setup。
