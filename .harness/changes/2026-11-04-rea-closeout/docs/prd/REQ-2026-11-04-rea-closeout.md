# REQ-2026-11-04-rea-closeout.md — REA 三问收口（RPM 阈值校准 / 代理观测面板可见 / 治理证据链机器化）

## 必要性（四问）

- **真实问题**：用户三连问「都做了哪些重构、达到什么效果、这个点是否真的有用、还能不能更好」——已落地的 REA 借用存在三处「机制就位但没到位」：
  1. RPM 硬闸是能力（默认 0 关），但「若开启设多少」无证据——校准只答了默认值，没答阈值（PLAN 补审结论原话「校准只回答了默认值，机制仍是缺口」的镜像：机制已补，阈值仍缺）；
  2. 代理 failed 只藏在 /status 细节里，面板首页零消费——「fail-closed + 三态观测」闭环缺最后一环（运维开面板看不见）；
  3. changes/*/summary.md 引用的 REQ/ADR 无机器检查——证据链断当场不报错，几周后以「AI 又做错了」显形（本仓 doc-drift 先例）。
- **不做会怎样**：阈值靠拍脑袋（误伤生产流量或形同虚设）；代理配错时运维只能翻 /status；证据链断链继续潜伏。
- **现状是否已变**：生产已有 8 天请求日志（10-28..11-04，6191 条 chat 请求）可直接算，无需挂新观测；/status 已恒输出 proxy_status（ADR-021）只差面板消费。
- **更便宜手段**：①阈值 = 只算数据写结论，零代码；③ = check-harness 加一检项（与检项 [1] 同族）；② = overview 注入一键（仿 KeyRPMStats 注入先例，后端 4 行 + 前端一段渲染）。均无新依赖、无架构改动。

## Given-When-Then 验收

1. **RPM 开启阈值已校准（数据留档）**
   - Given 生产 8 天请求日志存在；When 跑 `.harness/changes/2026-11-04-rpm-calibration/analyze_rpm.py`；
   - Then 输出每账号 60s 峰值/1s 峰值/间隔<1s 对数，与 summary.md 数据表一致；
   - And PLAN 的 RPM 行标注「开启阈值已校准：建议 40」（零误伤真实账号观测峰值 37）。
2. **代理状态面板可见（overview + 首页）**
   - Given Panel 接线 Upstream（生产恒接线）；When GET /panel/api/overview；
   - Then 响应恒含 `proxy_status` 键（未观测到代理 = `{}`，非 unknown）；
   - And 首页提示行渲染：failed 红字 + 悬停明细，active 计数；未接线时键省略（前端不打扰）。
3. **治理证据链机器化（检项 [10]）**
   - Given changes/*/summary.md 引用 REQ/ADR 文件名；
   - When 跑 check-harness.ps1；
   - Then 引用按「仓库根 + 变更目录内」双基地解析，任一存在即过；命令输出形态行（`[OK]/[FAIL]/$ `）豁免；含通配跳过；
   - And 全仓 100 处引用 0 断链、exit 0；pre-commit 检查 9 标题同步「10 检项」。

## 范围

- 动：`internal/panel/panel.go`（overview 增键）、`internal/panel/app_views_accounts.js`（首页渲染）、
  `scripts/check-harness.ps1`（检项 [10]）、`scripts/pre-commit.ps1`（标题同步）、
  `.harness/changes/2026-11-04-rpm-calibration/`（新增数据留档）、`docs/PLAN-magpie-borrow-2026-11-04.md`（RPM 行）。
- 不动：RPM 配置默认值（保持 0 关，阈值 40 是「需要时直接开」的已校准建议）；面板其他视图；/status 合同。
- 未决：阈值 40 是否启用由用户 config.json 决定（校准只给值）；探测类请求（/v1/models）不在 request-logs 内，频率上升需重校（unknown 已登记）。

## 不做 / 留档

- 告警推送（webhook/邮件）：面板可见是闭环第一步，推送触发条件（failed 持续时长）无观测数据支撑，留待面板上线后统计（requires：failed 态持续时间分布）。
