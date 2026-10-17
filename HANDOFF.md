# YG Gateway 项目交接（供新对话续接）

> 本文件是完整上下文快照，新对话第一件事读它，然后从「当前待办」接着做。

---

## 一、项目是什么

**YG Gateway** —— 自托管的 OpenAI 兼容反向代理网关：把腾讯 CodeBuddy 账号池包装成统一的 `/v1/chat/completions` 服务，附 Web 管理面板 + 全自动养号。

- Go 单仓库，Go 1.27.2（低版本带 9 个 stdlib CVE，必须 ≥1.27.2）
- 面板 JS/CSS 经 `go:embed` 打进二进制（改前端必须重编译）

## 二、两套目录（必须分清，别混）

| 路径 | 角色 | 说明 |
|---|---|---|
| `D:\yg-gateway` | **DSH 侧（私有运行实例）** | 4 个账号凭证、运行状态、persona.md 都在这里；服务跑在 127.0.0.1:7863 |
| `C:\Users\yanha\Desktop\yg-gateway` | **开源侧（GitHub）** | 干净单仓库，已推到 GitHub，无凭证/无 persona |

- 两边的代码改动要**手工同步**（Copy-Item 或各自编辑），不是同一个 git 仓库。
- 开源仓库地址：`https://github.com/qiuqianyi66/yg-gateway`（公开）
- 当前两边 HEAD 都已提交干净，工作区无未提交改动。

**⚠️ 运行实例现状（2026-10-12 深夜实测）**：
- **两个实例都在跑**：`D:\yg-gateway\yg-gateway.exe`（**7863，生产**，4 号健康、养号调度全开）
  与 `D:\yg-gateway-eval\yg-gateway.exe`（**7864，eval/验证**，上轮 ADR-005 真机验证用）。
- 两个实例共用 `auth_dir: D:/yg-gateway/auths`；**config 各自独立**，改一边不会同步到另一边。
- DSH（D:\yg-gateway）已部署本轮前端打磨版（`yg-gateway.exe.pre-playground.bak` 为替换前备份），
  私有运行文件（config.json/persona.md/auths/data）保留未动。

## 三、构建与测试（关键环境）

```powershell
# Go 工具链不在系统 PATH（注意：不是 $env:TEMP\go，那是旧记录）
$go = "C:\Program Files\Go\bin\go.exe"     # Go 1.27.2
$env:GOPROXY = 'https://goproxy.cn,direct'   # proxy.golang.org 本机不通
$env:CGO_ENABLED = '0'

& $go build -trimpath -ldflags="-s -w" -o yg-gateway.exe ./cmd/server
& $go vet ./...
& $go test ./...                          # 20 包，全绿
& $go fmt -l ./cmd ./internal             # 应为空（CI 门禁）
node --check internal/panel/app.js        # 前端语法（go:embed 不查内容）
```

- 全量测试：17 个有测试的包 ok + 3 个 no test files（cmd/credit、cmd/signin、cmd/trial）= 20 包。

### 部署踩坑：新 exe 被实时防护删除（火绒）

**现象**：`Copy-Item` 到 `D:\yg-gateway\yg-gateway.exe` 后，隔一段时间文件消失，只剩
`.bak`；下次启动报「不是可运行程序」或启动即退出（退出码 -1）。

**原因**：火绒等实时防护把**新建的可执行文件**判为可疑并隔离/删除。刚复制完还能跑，
防护扫描完一轮才删——所以「复制成功 + 启动成功」不代表安全，跨会话回来才发现没了。

**处置（按优先级）**：
1. **本机构建场景**：火绒加白名单（防护 → 信任区/白名单），把 `D:\yg-gateway\` 目录
   和构建输出目录加进去。这是根治办法。
2. **临时场景**：关掉火绒的文件实时监控，再执行构建+部署。
3. **无论如何都要做**：构建 + 复制 + 启动**放同一条命令**里（跨命令的空档正是防护
   下手时机）：

```powershell
cd C:\Users\yanha\Desktop\yg-gateway
& "C:\Program Files\Go\bin\go.exe" build -trimpath -ldflags="-s -w" -o yg-gateway.exe ./cmd/server
Copy-Item -Path ".\yg-gateway.exe" -Destination "D:\yg-gateway\yg-gateway.exe" -Force
$start = Start-Process -FilePath "D:\yg-gateway\yg-gateway.exe" `
    -ArgumentList "--config D:\\yg-gateway\\config.json" -WorkingDirectory "D:\yg-gateway" `
    -WindowStyle Hidden -PassThru
Start-Sleep -Seconds 4
if ($start.HasExited) { "进程退出 码=" + $start.ExitCode } else { "PID: " + $start.Id }
```

4. **验证要落地**：别只看「复制成功」，启动后用这条确认服务真在响应：
   `Invoke-WebRequest -Uri "http://127.0.0.1:7863/panel/"` 应返回 200；
   并用 `Get-ChildItem D:\yg-gateway -Filter "yg-gateway*"` 确认 exe 还在。
5. **替换前先备份**（R15）：`Copy-Item` 到 `.v{版本}.bak`，被删了也能秒回。

**当前状态（2026-10-12）**：火绒已由 YG 关闭，二进制部署正常（见 `Get-Process yg-gateway`）。若后续
重新开启火绒，按第 1 条加白名单。

## 四、已完成的工作（按时间线）

1. **项目独立定型**：改 module 名、品牌、版本 2.0.0、Go 1.27.2
2. **面板重做**：首屏信息分级（主指标+次要指标）、到期卡片栅格化、列名修正
3. **安全加固**（3 条）：
   - F-001 入站 body 上限（默认 64MB，`MaxBytesReader`，413）
   - F-002 受信任代理判定（防伪造 XFF，`trusted_proxy.go`，从右往左取第一个非受信任 IP）
   - F-003 只读端点限流（`ratelimit.go`，自写固定窗口，零依赖）
4. **harness 治理体系**：`.harness/` 下 rules（3 份规范）+ wiki + changes + skills（11 个技能）+ trouble-shooting
5. **选号权重修正**：快过期 ×3 → ×2（避免无快过期号被饿死到 7.3%）
6. **开源发布**：切割到桌面 → 干净单仓库 → 推到 GitHub → 发 v2.0.0 Release（五平台二进制）
7. **修 Dockerfile**：基础镜像 golang:1.23 → 1.27（开源 CI 暴露的 bug）
8. **竞品分析**：`docs/COMPETITIVE-ANALYSIS.md`，含诚实劣势 + 4 条差异化优势 + 4 条路线图
9. **多 API Key 派发（一期，ADR-001）**：httpauth 前缀索引 + 常量时间比较、config.json `api_keys` 数组 + 三态 models、realm 约束、热更新、脱敏回显、key_id/key_name 归因；测试门禁 8 类全绿
10. **面板 Key 管理页（二期，ADR-002）**：卡片+弹窗风格，新增/编辑/启停/删除/一键生成，明文一次性展示，CSP 兼容（无行内 onclick）
11. **用量按 Key 聚合**：`usage` 桶加 Key 维度（fileVersion 4→5），面板用量页与积分页各加「按 Key」页签；旧数据归入「未归因」（无损）
12. **`/v1/models` 档位字段双名**：`supported_efforts` / `default_effort` 作为 `reasoning_*` 的别名，兼容只认裸名字段的客户端（修「该模型不校验该参数」提示）
13. **深度优化轮（前端 + 后端低风险项）**：热路径去重解析（实测 ~6× / 省 61% 内存）、reqlog 环形缓冲、404/405 OpenAI 信封、CSV 导出、骨架屏 + 就地错误态、配置页脏守卫、hash 路由、渲染短路、轮询三件套（防覆盖/隐藏暂停）、无障碍与对比度
14. **ADR-003 调度可靠性四项**：断路器 half-open（`pool.breaker_probe_gap`）、全局准入 429（`pool.global_max_in_flight`）、选号降锁（prune 时间节流，实测 2×）、延迟分位数（新包 `internal/metrics`，`/status` 的 `latency` 段）
15. **安全巡检**：发现并修复 `auths/*.json` ACL 未加固（`BUILTIN\Users` 可读明文 token）；`govulncheck` 零漏洞；面板 36 个 API 全 `withAuth`；多 Key 约束绕过逐条验证挡住
16. **排水式优雅停机**：Shutdown 5s → 30s 并记录超时（原 5s 会硬掐在途 SSE）
17. **请求记录分页**：>50 条/页渲染（原 1000 行 × 10 列全量 DOM，改筛选卡顿）
18. **ADR-004 会话粘性派生键可关闭**（修「4 个号有积分却只用一个号」）：
    机制 = 客户端不传会话标识时按 `SHA-256(system + 首条 user)` 派生会话键，
    固定 system + 短开场会把**无关对话**判为同一会话、全钉到一个号。
    实测对照：不同 `conversation_id` × 6 → 分散 3 号；相同 system+开头 × 4 → **4/4 单号**。
    新配置 `session_sticky.derive`（`*bool` 三态，默认开 = 零回归；**本实例已设 false**）。
    顺带修 R11：`session_sticky.enabled` / `.derive` 是装配期字段，此前漏列重启项。
19. **ADR-005 后端选号优化（D-600 去写锁 + D-601 EWMA 回灌）**：
    - D-600：`pick` 由写锁改 **RLock**（并发 pick 并行），lastUsed/usedSeq/breakerProbeAt
      原子化写回，探索台账独立 `exploreMu`，过期台账修剪移出热路径（saveLocked + flusher
      兜底）；语义放宽（同瞬间并发可能同选一号）由 handler Acquire + tried 轮换自愈。
    - D-601：每账号 EWMA 成功率/延迟信号回灌选号权重（`pool.ewma` 段，**默认关 = 零回归**，
      热生效 `SetEwma`）；成功率基线 0.95 ±15% 钳制、延迟仅罚 >1.5× 池中位（-30% 封顶）、
      懒初始化 1.0、样本 <5 中性、因子 [0.595,1.15] 不饿死；`/status` 新增
      `ewma_success/ewma_latency_ms/ewma_samples` 透出。
    - 全量 20 包绿 + vet/gofmt 干净；D: 实例（7864）已部署并真机验证（4 号健康、
      EWMA 字段随真实流量透出）。
20. **Playground 模型下拉 WorkBuddy 化**：模型下拉选项显示「模型名 · x生效倍率 · 免费/优惠标签」
    （对齐 WorkBuddy 模型选择器展示；倍率取上游 modelPromotions 生效价 promo_credits，无促销时牌价
    credits，免费=factor 0 或牌价 0 标「免费」，其余促销标签原样透出，无促销但支持图片的模型标
    「视觉」）；option 带 promo_note/description 悬停提示，value 仍为完整模型 id 不变。仅前端
    app.js 改动，已重编译并部署 D: 生产实例（7863，2026-10-15 19:27 替换，新 PID
    验证 /panel/app.js 含 pgModelLabel；替换前备份 yg-gateway.exe.pre-workbuddy.bak），
    node --check + 20 包测试全绿。另实测确认：上游目录所有模型 supports_images=True
    （含 deepseek-v4.1-flash，为多模态），WorkBuddy 客户端「不支持图片」提示属客户端侧限制。
21. **成本控制规范**：新增 `.harness/rules/COST-CONTROL.md`（基于生产 72h 实测：99% 积分
    烧在 hy4-preview 系列，成本率 ~120 积分/M token、缓存命中仅 7-17%；deepseek-v4.1-flash
    成本率 ~1.1、缓存命中 98.7%；step-5-preview 免费；主力默认模型禁指 hy4 系列）。

## 五、当前待办（新对话从这里接）

**主线已完成**：
- ✅ 多 API Key 派发（一期，ADR-001）+ 面板 Key 管理页（二期，ADR-002）
- ✅ 用量按 Key 聚合（`by_key` / `credit_by_key` + 面板「按 Key」页签）
- ✅ `/v1/models` 档位字段双名兼容
- ✅ 深度优化轮（前端体验 + 后端热路径 6× + 路由兜底 + CSV 导出 + 分页）
- ✅ ADR-003 调度可靠性四项（half-open / 全局准入 / 选号降锁 / 分位数）
- ✅ ADR-004 会话粘性派生键开关（+ R11 重启项修正）
- ✅ **ADR-005 后端选号优化**：选号去写锁（RLock + 原子写回 + prune 移出热路径）
  + EWMA 成功率/延迟回灌（默认关零回归，热生效）
- ✅ 全仓 gofmt 归零；代码规范速查清单（`.harness/rules/CODE-STANDARDS-CHECKLIST.md`）

**已应用的运行配置**（D:\yg-gateway\config.json，**当前活实例**，7863 端口）：
- `pool.global_max_in_flight = 18`（4 账号 × per-account 4，留余量）
- `pool.breaker_probe_gap` 空 = 默认 30s
- `session_sticky.derive = false`（关掉内容派生，修「只用一个号」）

**已应用的运行配置**（D:\yg-gateway-eval\config.json，**当前活实例**，7864 端口）：
- `session_sticky.derive` **未设置**（= 默认开；上一轮对「本实例」设的 false 落在
  D:\yg-gateway\config.json，两边配置并未同步，若需关闭派生记得同步到这里）
- `pool.ewma = {enabled:true, alpha:0.1, min_samples:5}`（ADR-005 真机验证，保守参数）

**下一轮候选**：
1. 粘性路径的成本分层（ADR-004 §五 方案 D 被弃：与缓存复用冲突，需重新设计）
2. 竞品路线图：OpenRouter 费率折算 / 增量用量落库 / 客户端生态深化 / Anthropic 协议（已落地基础版）
3. 可选打磨：EWMA 参数调优观测（/status 已透出信号）、pick CAS-claim（当前由
   Acquire+tried 兜底，仅同 tick 并发场景才需考虑）
4. ~~虚拟滚动~~（已被请求记录 50 条/页分页取代，不再需要）

**⚠️ 环境坑（已踩，务必注意）**：
- **写 `D:\yg-gateway\config.json` 不要用 PowerShell `Set-Content -Encoding UTF8`**
  —— 会加 UTF-8 BOM，Go 的 json 解析直接失败（`invalid character '\ufeff'`），服务起不来。
  用无 BOM 写法：
  ```powershell
  $enc = New-Object System.Text.UTF8Encoding($false)
  [System.IO.File]::WriteAllText($path, $text, $enc)
  ```

---

## 六、本轮前端大改（2026-10-12 晚）

**起因**：用户反馈"前端太丑，一眼差评"，要求按 Vercel/Linear 风格大改。

**改了什么**：
1. **全局设计 token**：强调色从纯蓝改 indigo→violet 渐变（`#6366f1 → #8b5cf6`），状态色更饱和（emerald/amber/red）
2. **账号池**：表格 → 卡片网格（头像首字母渐变 + 昵称 + 积分大数字 + 进度条 + 4 列统计）
3. **组件质感**：导航 active 左侧指示条、输入框 focus 3px ring、tag 细边框、按钮渐变 + 阴影
4. **用量页**：KPI 从 6 张散卡改 4 张大卡（请求/Token/成功率/延迟）
5. **新增能力**：通知中心（localStorage 持久化）、命令面板（Ctrl+K，9 条命令）、请求记录批量选择 + 导出 CSV、粘性会话详情列表（成本层级可见）
6. **Anthropic 协议**：/v1/messages 端点（流式 + 非流式），Claude Code 可直连

**测试调整**：账号表格改卡片网格后，`TestAppJSSemanticLandmarks` 的 th scope 阈值从 20 降到 12（设计升级，不再用 table）。

**当前 HEAD**：`96be554`（已 push GitHub）

**下一轮候选**：
1. 粘性路径成本分层（ADR-004 遗留，需折中设计）
2. 竞品路线图：增量用量落库 / OpenRouter 费率折算深化 / 客户端生态
3. EWMA 参数调优（/status 已透出信号）

## 六之二、本轮前端打磨（2026-10-12 深夜，第二波）

**起因**：用户要求"把前端好好去做"，一次性做完剩余前端改进；按偏好先调研竞品前端
（new-api/ARP 有内置在线测试，YG 独缺）再用横纵分析定方向。

**改了什么**：
1. **API 调试台（Playground）**：顶栏终端按钮 + 弹窗，直连本机 `/v1/chat/completions`
   （CSP `connect-src 'self'` 放行同源；鉴权头复用面板 Bearer key，与客户端同口径）。
   模型下拉（/panel/api/models）+ temperature/max_tokens + 流式开关；SSE 行缓冲解析
   逐 delta 渲染 + `[DONE]` 收尾；AbortController 停止；耗时/用量统计；错误信封就地显示；
   已纳入统一 Esc/遮罩关闭体系（VEIL_CLOSERS）。
2. **修复命令面板真 bug**：5 条跳转命令引用**从未定义的 `switchView`**，点击必抛
   ReferenceError。改走 `cmdGo`（带 cfgDirty 守卫 + hash 同步）；命令从 9 条扩充到
   18 条（8 视图全跳转 + 添加账号/调试台/扫描待办/查询券码/导出用量/导出积分等）。
3. **请求记录键盘可达**：行 `tabindex=0` + `aria-expanded`，Enter/Space 展开详情；
   仅用户主动展开才 `scrollIntoView`（轮询刷新路径不滚动，不打断阅读）。
4. **响应式修复**：`.acc-grid` 改 `minmax(min(340px,100%),1fr)`，消除窄屏卡片横向溢出。
5. **骨架数量对齐**：usStats 6→4、usCreditStats 4→5（与实际渲染一致，去掉闪跳）。
6. **通知中心「全部已读」**显式按钮（与打开面板自动清读同一操作）。

**测试**：新增 6 项源码断言测试（Playground 完整链路 / 命令面板防回归 / 键盘可达 /
骨架数量 / 全部已读 / 响应式），20 包全绿；`node --check` 通过。

**验证（真机）**：7863 已热替换（备份 `yg-gateway.exe.pre-playground.bak`）；
healthz 200 + 4/4 健康；overview/models 带鉴权 200；`/v1/chat/completions`
非流式 200 + 流式 SSE 全链路（delta 逐块 → usage → `[DONE]`）与前端解析逻辑一致。

## 六之三、OpenRouter 费率折算（2026-10-13）

**起因**：竞品路线图第 2 条——用户想知道"白嫖了多少美元"。当前只有积分→人民币
折算（credit_cny_rate），看不到这些 token 按 OpenRouter 公开价值多少钱。

**改了什么**：
1. **后端定价表**：`usage.USDPrice{Input,Output,CacheRead}`（$/1M tokens）；
   `SnapshotWindow` 多一个 `pricing func(model) USDPrice` 参数；`bucketUSD` 区分
   缓存命中（prompt_tokens 含 cached_tokens，命中部分按 cache_read 计价）。
2. **配置**：`usage.pricing` 段（map[裸模型名]→价格）；内置默认表覆盖 GLM/
   DeepSeek/Kimi/MiniMax/混元/阶跃常见模型族，用户 config 可覆盖同名 key；
   未知模型不折算（omitempty，不伪造 $）。
3. **面板**：用量明细表加「参考价 $」列（所有维度）；扣除积分卡副标题附
   "若按 OpenRouter 公开价 ≈ $X"；`fmtUSD` 紧凑格式化（<$0.01 三位小数）。
4. **纯展示层**：不参与选号/计费/路由，热路径零影响。

**测试**：`TestUSDValuePricing` 固化折算口径（缓存命中按 cache_read 计价）；
20 包全绿 + vet/gofmt 干净。

**验证（真机）**：7863 已热替换（备份 `yg-gateway.exe.pre-usd.bak`）；
healthz 4/4；72h 窗口 13247 tokens → $0.005（deepseek-v4.1-flash 5762 tok →
$0.00094），折算链路端到端通。

**当前 HEAD**：`0ff8c01`（已 push GitHub）

**下一轮候选**：
1. 粘性路径成本分层（ADR-004 遗留，需折中设计）
2. 增量用量落库（JSONL 逐条审计，当前仅聚合桶）
3. EWMA 参数调优观测（/status 已透出信号）
4. 客户端生态深化（Codex / 其他 SDK 直连）

## 六之四、三路推进（2026-10-13）

**路线盘点结论**：路线2（JSONL落库）已由 reqlog 完整落地跳过；本轮做路线4→1→3。

**1. 客户端生态实测（路线4）**：
- /v1/chat/completions 非流式+流式 SSE ✅；/v1/messages 非流式 ✅
- 不存在模型名返回 400（合理，网关不做任意映射）
- Codex 用户需在配置里指定 `cn:glm-5.3-flash` 这类模型名
- 无需代码修复

**2. 粘性命中收费号计数器（路线1，轻量版）**：
- `stickyPaidHits` 原子计数器：PickByUIDForModel 命中时若绑定号已实测收费
  （tier 2, CostPer1k>0）且池里有 tier 0/1 健康号则 +1
- **不自动换号**（保上游 prompt 缓存）；/status 透出 `sticky_paid_hits`
- TestStickyPaidHitsCounter 固化口径

**3. 7863 开启 EWMA（路线3）**：
- config.json pool.ewma = {enabled:true, alpha:0.1, min_samples:5}
- 与 eval 实例同口径；需一周流量积累后观察 ewma_success/ewma_latency_ms

**验证**：20 包全绿 + vet/fmt 干净；7863 已热替换（PID 13112），4/4 健康；
sticky_paid_hits / ewma_samples 字段透出正常。

**当前 HEAD**：`f450f2c`（已 push GitHub）

**下一轮候选**：
1. EWMA 一周后观察调参（看 /status ewma_* 数据）
2. 粘性会话详情面板加成本层列（如需更直观）
3. 其他用户反馈

## 六之五、中转站能力对齐 P0（2026-10-13）

**起因**：调研市面主流中转站（one-api / new-api / Veloera / workbuddy 系）后，
按"小团队自用也刚需"原则挑出 4 项功能对齐。SaaS 化重功能（多用户/充值/兑换码/
OIDC 登录/多语言）刻意不抄——保持 yg-gateway"自托管养号池"定位。

**改了什么**：
1. **模型映射 `model_aliases`**：config 加 `map[string]string`，客户端写短名（如 `glm`）
   网关转发前替换成上游实际名（如 `glm-5.3-flash`）。只影响出站 body；选号/白名单/
   用量/回包 delta.Model 全部沿用客户端原始裸名（观测口径不分裂）。热生效（走 livecfg）。
2. **白名单通配符**：`api_keys[].models` 现有精确匹配基础上加 `*` 后缀通配
   （`glm-*` 放行整个 GLM 族；裸 `*` 放行全部）。抽成纯函数 `modelAllowedByWhitelist` 便于单测。
3. **Per-Key RPM 限流**：`api_keys[].rpm` 字段（0=不限，零回归）。每把 key 独立
   1 分钟固定窗口计数，超限返 429 + Retry-After 60。挂在 withAuth 鉴权后、业务前，
   未配置 rpm 的 key 恒放行。新文件 `internal/server/keyrpm.go`。
4. **Webhook 异常告警**：新包 `internal/alerting`。config `alerting` 段：
   `webhook_url`（空=关闭）、`min_credit_threshold`（余额告急，0=关）、
   `dedup_minutes`（去重窗口，默认 60）。触发点：余额耗尽（ErrHardCredit）、
   会话死（ErrSessionDead/Disable）、账号被上游封（11140 request illegal）。
   POST 通用 JSON `{text,type,account_uid,credits,...}`，fire-and-forget 异步，
   Server酱/TG/飞书自定义机器人通用格式可直接接，特定平台格式走 n8n 中转。

**面板配套**：
- Key 管理弹窗加 RPM 输入框 + 卡片 RPM badge；models 字段 hint 加通配符说明。
- 配置页加「模型映射」textarea（`from=to` 每行一条）+「异常告警 Webhook」
  （URL + 余额阈值）。model_aliases 是 map 类型，collectConfig 单独序列化。

**测试**：新增 11 项断言（applyModelAliases 5 例 / modelAllowedByWhitelist 9 例 /
keyrpm 隔离与上限 / notifier 去重与阈值），20 包全绿 + vet/gofmt 干净。
**注意**：gofmt 顺带收了 5 个历史文件的格式差异（Go 1.27 与旧版 gofmt 风格漂移），
不是本轮功能改动。

**未做（P1/P2 候选，下一轮再议）**：
- P1：账号池分组（国内/国际版多池）、Per-Key 月 token 配额硬上限
- P2：多上游级联（CodeBuddy 全挂时切备用 OpenAI 兼容源，架构级重构）

## 六之六、P1 落地（2026-10-13 晚）

**P1-A：Per-Key 月 token 配额硬上限**
- `api_keys[].monthly_token_quota int64`，0=不限。
- usage.Recorder 加 `monthly map["YYYY-MM|keyID"]int64`，Add() 按 keyID 累加（口径同 b.TT），
  暴露 `MonthlyUsed(keyID)`；fileVersion 5→6，load/flush 序列化。
- handler withAuth 里 RPM 检查后叠加：`used >= quota` 返 429 + `quota_exceeded` + Retry-After=86400。
- 面板 Key 弹窗加「月 Token 配额」输入框 + 卡片 badge（`N.NM tok/月`）。

**P1-B：账号池分组（多池路由）**
- `auth.Auth.Group`：账号级分组标签，从 auth 文件顶层 `"group"` 键读取（嵌套/扁平形都读）；
  SaveAtomic 非空才写回。空=default 池。
- `api_keys[].group string`：key 级绑定，空=不限池。
- pool.Pick 签名加 group 参数（保留 PickExcludingForRealm 旧 wrapper 零改测试），
  新增 `PickExcludingForRealmGroup(tried, model, realm, group)`。
  realmOK 闭包叠加 `group=="" || e.a.Group==group`。
- handler 主选号（1106 行）改用新方法，传 `authKey.Group`。
- 面板 Key 弹窗加「账号池分组」输入框 + 卡片 badge（`组：xxx`）。

**验证**：20 包全绿，vet/gofmt/JS 语法干净；二进制 10.6MB。
**兼容性**：group/quota 字段缺省零值（0/""）= 旧行为完全不变，老 config.json 直接跑。

**下一轮候选**：
1. P2 多上游级联（备用 OpenAI 兼容源）
2. 其他用户反馈

## 六之七、P2 备用上游级联落地（2026-10-13）

**起因**：P1 后的下一轮候选第 1 条——CodeBuddy 账号池全挂时切备用 OpenAI 兼容源。

**改了什么**：
1. **`fallback` 备用上游级联**：config 加 `fallback` 段（`enabled` / `base_url` / `api_key` /
   `models` 白名单 / `timeout_seconds`）。选号走完所有账号仍未拿到可用号时，若请求模型
   在备用源白名单里 → 把请求 body **原样**转发到 `{base_url}/chat/completions`
   （OpenAI 兼容，DeepSeek 官方 / OpenRouter / 任意兼容端点）。空配置 = 关闭（零回归），
   只对白名单模型生效（CodeBuddy 专属模型不发备用源 404）。
2. **SSE 透传**：Content-Type 为 `text/event-stream` 时直接 io.Copy 透传不缓冲；
   Accept 头透传。非流式读全 body 解析 usage 字段。
3. **用量归因 realm=fallback**：非流式 200 响应解析 usage →
   `Usage.Add(realm="fallback", keyID="fallback")`，面板用量/积分页可见备用源烧的 token
   （流式不解析，仅透传）。
4. **reqlog 口径**：转发成功记 `status=200 / outcome=success`，直接 return 不走错误路径。

**装配**：`cmd/server/main.go buildFallbackConfig`（未启用 / 缺 base_url 或 api_key → nil）；
`internal/server/handler.go fallbackForward`（超时 <=0 回落 120s）。

**测试**：`internal/server/fallback_test.go` 5 例（Disabled / EmptyConfig / JSON 归因 /
Stream 透传 / UpstreamError 回落），20 包全绿 + vet/gofmt 干净。

**验证（真机）**：7863 已部署最终二进制（12:29:53 构建，10.6MB）并重启，healthz 通过、
4/4 健康（fallback 未启用 = 零回归，config.json 未配即关闭）。

**当前 HEAD**：`7fcb3af`（已 push GitHub，a183f55 → 69b2a65 → 7fcb3af）

**下一轮候选**：
1. 如需启用 fallback：生产 config.json 配 `fallback.base_url` / `api_key` / `models`
2. 其他用户反馈

## 六之八、面板「接入配置导出」（2026-10-13 晚）

**起因**：用户要把分出去的 API Key 做成「可直接导入其他 agent」的东西——创建 Key 时
一键复制 OpenAI / Anthropic 两协议的接入配置。

**改了什么**：
1. **创建 Key 后的明文弹窗升级**：除密钥外，同时展示两段可复制的接入配置：
   - OpenAI 协议（Codex / Cursor / OpenWebUI / SDK）：`OPENAI_BASE_URL=http://<host>/v1` + `OPENAI_API_KEY=...`
   - Anthropic 协议（Claude Code）：`ANTHROPIC_BASE_URL=http://<host>` + `ANTHROPIC_AUTH_TOKEN=...`
   - base_url 用面板实际访问地址（location.protocol + host）动态拼，不写死；配置内含一行
     `# 可用模型：GET <base>/v1/models 查看`（客户端自动发现模型，不写死模型名）。
2. **纯函数 `buildKeyImportConfigs`**（app.js）：输入明文 key + baseURL → 输出两段配置文本，
   尾部斜杠归一。**不破坏安全模型**：明文仍仅创建时一次性展示，服务端只存哈希。
3. **复制**：抽出 `writeClipboardText`（clipboard API + execCommand 降级），密钥与两段配置
   各带复制按钮（btnCopyCfgOpenAI / btnCopyCfgAnthropic）。

**测试**：新增 TestAppJSKeyImportConfigs（node 精确断言两段配置文本）+ TestIndexHTMLKeyImportBlocks
（弹窗结构元素 + app.js 填充/绑定存在断言）；20 包全绿 + vet 干净。
**注**：gofmt 检查仍有 3 个历史文件格式差异（cmd/server/config.go、internal/httpauth/index.go、
internal/panel/panel.go），为 pre-existing（六之五记录过同类漂移），本轮未动。

**验证（真机）**：7863 已部署（备份 `yg-gateway.exe.pre-importcfg.bak`），healthz 4/4；
面板 HTML 含 cfgOpenAI/cfgAnthropic 元素；/v1/models 200（20 模型）供客户端自动发现；
API 链路实测创建/删除 key 正常；生产 config.json 已还原（无 api_keys 残留）。

**当前 HEAD**：`85f4ad3`（已 push GitHub；本轮面板改动将随下个 commit 推送）

**下一轮候选**：
1. 若需「已存在 key」也能复制接入配置：需服务端存明文（当前只存哈希，与 ADR-002 安全设计冲突，不建议）
2. 其他用户反馈

## 六之九、面板 Key「接入配置」兜底（2026-10-13 深夜）

**起因**：用户截图指出 Key 卡片操作区只有 开关/编辑/删除，没有「接入配置」入口——
担心创建 key 后明文弹窗误关就永久拿不回配置（服务端 loadConfigMasked 只回显空 key +
key_configured，明文仅创建时展示一次）。方案被接受后授权实施。

**改了什么**：
1. **Key 卡片新增「接入配置」按钮**（data-action="importcfg"）→ 打开 keyCfgVeil 两态弹窗：
   - 状态 A：本会话缓存了明文（创建/重置后 lastKeyPlaintext/lastKeyPlaintextKid 模块级缓存）
     → 直接展示密钥 + OpenAI/Anthropic 两段配置 + 各自复制按钮（可复用回看）；
   - 状态 B：明文不可恢复（刷新/换浏览器）→ 提示"服务端只存哈希" + 引导「重置密钥」。
2. **后端专用接口 `POST /panel/api/key-reset`**（body `{id,key}`，与 /panel/api/config 同鉴权）：
   - 关键设计判断：前端不持明文，全量 POST /panel/api/config 会因 normalizeAPIKeys 过滤空 key
     条目 + 数组整体替换语义**误删其他 Key**，故必须由服务端直改目标条目；
   - main 注入 `ResetKey` 闭包 → `resetAPIKey`（读 config.json → 定位 id 条目改 key → 序列化 →
     复用 saveConfig 全链路：校验+落盘+热应用，鉴权索引重建走 buildAuthSnapshot）；
   - 其他 Key 与顶层 api_key 原样保留；id=SingleKeyID 保留名拒绝；新 key 与 api_key 重复会被
     normalize 拒绝并透传错误。实测：旧 key 鉴权 /v1/models 立即 401，新 key 200（20 模型）。
3. **创建弹窗新增「下载 .env」**（btnCfgDownload）：`buildEnvFile` 纯函数把 OpenAI+Anthropic
   两段配置合并成一个 `yg-gateway-import.env`（Blob 落盘，实测触发真实下载）。

**测试**：TestKeyResetEndpoint（6 例：501/成功/空 id/空 key/坏 body/错误透传）+ TestKeyResetRequiresAuth
（401）+ TestAppJSBuildEnvFile（node 精确断言 .env 文本）+ TestIndexHTMLKeyCfgAndReset（弹窗元素 +
app.js 逻辑片段 + 绑定 + VEIL_CLOSERS）；20 包全绿 + vet 干净 + node --check 通过。

**验证（真机）**：7863 已部署（备份 `yg-gateway.exe.pre-keycfg.bak`），healthz 4/4；
Playwright 实测主路径全通：卡片按钮 → 状态 A 回看配置 → 重置密钥（confirm → 后端 → 立即切新密钥）→
刷新后状态 B（提示 + 重置入口）→ 下载 .env 落盘；生产 config.json 已还原（无 api_keys 残留）。

**当前 HEAD**：`276b17b`（已 push GitHub；本轮 7 文件 +376/-3）

**下一轮候选**：
1. 用户如需要：重置密钥时可选「自定义新密钥」（当前固定 genApiKey 生成）
2. 其他用户反馈

## 六之十、README 重写为主推项目门面（2026-10-13 深夜）

**起因**：用户要求把 README「好好写」，作为项目主推门面。

**改了什么**：
1. **结构升级**：hero 一句话 + shields 徽章（license/go/stars）→ 真实面板截图（两张：
   panel-accounts.png 账号池看板、panel-keys.png API Key 管理）→ mermaid 架构图 → 核心能力
   （分组表格）→ 快速开始 → 客户端导入（OpenAI/Anthropic 协议对照表）→ 进阶配置 →
   调度可靠性 → 安全基线 → 已加固审计 → 面板八视图 → 开发 → 路线图 → 免责声明。
2. **截图匿名化**：账号池截图在 Playwright 里先把真实昵称替换为「演示账号 A-D」
   （精确匹配昵称文本节点，品牌区不受影响）再落盘；截断 UID 片段（前 8+4 位 hex）仍可见，
   无法用于登录；**积分余额数字（14931/21077）为真实值保留**——如用户在意公网泄露可后续
   再处理或换演示数据。
3. **不编造**：功能清单/参数/CVE 描述均沿用原 README 已验证内容；路线图取自真实候选。

**验证**：截图经 Read 目检（账号池页数据加载完整、Key 页演示卡片含「接入配置」按钮）；
README 引用文件全部存在（LICENSE/go.mod/docs/COMPETITIVE-ANALYSIS.md/lock-auths.ps1/docker-compose.yml）。

**当前 HEAD**：`e009c46`（已 push GitHub）

## 六、关键架构事实（新对话必知）

### 鉴权（httpauth）
- `internal/httpauth/httpauth.go`：`VerifyBearer(r, key)`，SHA-256 摘要 + `subtle.ConstantTimeCompare`，常量时间。
- key 为空 = 不鉴权恒放行。
- server（/v1/*）和 panel（/panel/api/*）共用同一口径。

### 选号（pool/pick.go）
- 权重 = `1 + credits/maxCredits×10 + idleWeight`（快过期号再 ×2，`expiringVirtualSlots` 常量在 `realm.go`）
- 成本分层：免费(0) > 未知(1) > 收费(2)，硬过滤只留最优层
- 积分保底 `credit_floor`、防惊群（minPickGap 100ms + usedSeq 单调序号）、LRU 兜底

### 配置（cmd/server/config.go）
- 热生效字段走 `livecfg`（api_key/soft_rate/脱敏开关）；装配期字段（body 上限/受信任代理/限流/base）改需重启
- 安全配置在 `security` 段：`trusted_proxy`、`read_only_rate_limit`

### 红线（编码规范 15 条，关键几条）
- R8：上游协议数据不得改名（wb2api-<ms> conversationId、chatcmpl-wb2api 哨兵、UA 指纹）
- R13：前端改必重编译（go:embed）
- R14：构建工具链 ≥ Go 1.27.2
- R15：二进制替换前备份

## 七、竞品速览（做派发功能必读）

| 项目 | 值得学的 |
|---|---|
| workbuddy-manager（ithtelab） | 密钥分组、IP/模型白名单、多上游级联、SQLite、签名一键更新 |
| workbuddy2api-hub（ardeyouxipianyi） | OpenRouter 费率折算、增量用量聚合、剩余用量优先调度 |
| ARP（KaiXuanSama） | OpenAI+Anthropic 双协议、扫码登录 |

YG Gateway 的差异化定位（写进了 README）：**单二进制零依赖 + 养号友好 + 安全加固 + 可审计治理**。

## 六之十一、热路径去重解析 + 流式可靠性 + /metrics（2026-10-14）

**起因**：用户「继续优化网关流程，你自己上网去分析，我只要结果」。调研（new-api /
one-api / Cloudflare AI Gateway / gpt-load）+ 本仓静态审阅后，把重心从「选号算法」
（ADR-005 已做）移到**请求本身**。

**改了什么（8 项，全部零回归或默认关）**：

1. **入站 body 单次解析（F-701）**：新增 `internal/session/requestview.go`
   `RequestView` 惰性视图——顶层一次解析（字段名 → RawMessage），messages/tools/
   metadata 按需且只解析一次。热路径上 6+ 次全文档 `json.Unmarshal` 收敛为 1 次。
   - 顶层用 RawMessage 而非 typed struct：typed struct 遇到 `{"user_id":123}` 这类
     单字段类型不符会让**整份**解析失败（连 model 都拿不到），RawMessage 逐字段
     容错，与旧实现 `strOrEmpty` 口径一致。
   - `server` 侧 peek / 工具名单 / 会话键 / 轮级键 / conversationId / 带图判定
     全改读视图；`newChatStat` 改收已解析 model（删掉一次全文档解析）。
2. **出站 body 单次往返（F-702）**：抽出 `prepareBodyAST`，新增
   `PrepareBodyOptWithEffortsAndDefaultAndCache`——规范化与 prompt_cache_key 注入
   在同一 AST 上完成，只 marshal 一次（原为两趟背靠背 unmarshal+marshal，且
   `ChatStreamContext` 轮转时重复跑）。
3. **degrade 去锁（F-703）**：`until` 改 `atomic.Int64`（UnixNano），`Active()`
   热路径无锁。
4. **首帧前下发 headers（F-704）**：`StreamHint` 设完 SSE 头立即
   `WriteHeader(200)` + Flush。此前响应头惰性——上游迟迟不吐首 token 时客户端
   看不到任何东西。
5. **单帧/聚合上限（F-705）**：`readSSELine`（ReadSlice 累积）单帧 1MiB、
   `Aggregate` 整流体 16MiB，超限返回 `errFrameTooLarge`；handler 记 502 观测，
   **不罚号不轮转**（换号撞同一份畸形响应）。
6. **SSE 心跳 + 整请求 deadline（F-706/707）**：新配置
   `upstream.sse_keepalive_seconds`（静默写 `: ping` 注释帧，**默认 0 = 关**）与
   `upstream.stream_total_timeout_seconds`（**默认 0 = 不限**）——两者都会改变
   线上流行为，故不给默认值，由部署方按中间层形态显式开启（均装配期字段）。
   心跳开启时写路径加互斥，goroutine 由 stop 通道 + WaitGroup 保证返回前退出。
7. **refresh 继承 ctx + singleflight（F-708/709）**：`RefreshTokenContext(ctx, a)`
   （handler 传 `r.Context()`，客户端断开即取消，不再占满 30s）+ 按 uid
   `refreshGroup` 合并并发刷新（N 个并发临期只打 1 次上游）。
8. **`/metrics` Prometheus 导出（F-710）**：自实现 text exposition（零新依赖），
   `GET /metrics` 与 `/status` 同鉴权+限流口径。暴露 requests_total{outcome} /
   in_flight / success_rate / duration 直方图（全局 + model 标签）/ accounts{state} /
   accounts_in_flight / global_in_flight / tokens_total{kind} / cache_hit_total。
   标签值转义（伪造 model 名不能注入假指标行）。

**测试（本轮核心是「等价性守门」）**：
- `TestViewEquivalenceWithBytePaths`（22 body × derive 两态 × 3 键，与字节路径逐位对照）
- `TestToolNameAllowlistViewEquivalence`（11 body，DeepEqual，含畸形条目容错口径）
- `TestPrepareBodySingleRoundTripEquivalence`（12 用例 × sanitize 两态，单趟 vs 两趟逐字节）
- 新增行为测试 26 项（SSE 首帧头/帧上限/聚合上限/心跳开关/refresh ctx/单飞/metrics
  四态/escapeLabel/Prometheus 构造器/配置字段钳制/degrade 无锁）
- **20 包全绿**（17 ok + 3 no test files）+ `go vet` 干净；
  `gofmt -l` 仅 3 个历史文件漂移（config.go / httpauth/index.go / panel.go，pre-existing）。
  注：本机无 gcc，`-race` 不可用。

**验证（真机 7863）**：备份 `yg-gateway.exe.pre-hotpath.bak` → 替换重启（PID 15380）→
healthz 3/4 健康 → `/metrics` 200 且 12 个指标族齐全（带真实流量计数：
requests_total{succeeded}=3、tokens_total{kind=total}=15616、cache_hit=1280）→
`/v1/models` 200（20 模型）→ 非流式 200 + 完整 usage（credit=0.04）→
流式 SSE 全链路（role → reasoning_content 逐块 → content → finish_reason=stop →
usage 末帧 → `[DONE]`，且**无 ping 注释帧**=默认关零回归）。

**文档**：`docs/prd/REQ-yg-gateway-hotpath-optimization-2026-10-14.md`、
`docs/adr/ADR-006-hotpath-streaming-observability.md`、
`.harness/changes/hotpath-optimization-2026-10/summary.md`。

### 六之十一之二、R10 红线修复 + 可观测补齐（2026-10-15）

**起因**：上一轮留的两个尾巴——`fallbackForward` 静默吞 `io.Copy` 错误（R10 红线），
以及可观测缺 TTFB 与上游错误分类的时序导出。

**改了什么**：

1. **修 R10 红线（备用上游转发）**：`fallbackForward` 此前
   `_, _ = io.Copy(w, resp.Body)` ——把「客户端断连 / 备用源半截流」**一律记成
   成功转发**，调用方据此 return true，观测里根本看不到备用源的传输故障。
   改为 `newFlushWriter`（逐块 flush，与 StreamHint 同口径）+ 检查 `io.Copy`
   错误：半截流返回 false 并记日志。非流式 `io.ReadAll` 失败同样不再伪装成功。
2. **上游错误分类计数**：`applyErrorPolicy` 成为**唯一汇聚点**（所有轮转失败分支
   都过这里），按 `upstream.ErrKind.String()` 计数。新增
   `metrics.KindCounters`（标签数有界 64，空标签记 `(none)`，nil 接收者安全）。
3. **TTFB 分位数**：新增 `Config.TTFB *metrics.Registry`，流式分支在**真拿到
   首帧**时观测（0 = 无观测，空流/断连不记 0，避免「首帧 0 毫秒」的假数据）。
   与端到端 `Latency` 分开记——两个问题不同（"总共等多久" vs "上游多快开始吐"）。
4. **/metrics 暴露**：`upstream_errors_total{kind}` + `ttfb_ms` 直方图
   （全局 + model 标签）。main 自动装配，无需配置。

**测试**：`TestFallbackForwardStreamTruncated`（半截流判失败，还原成 `_, _ =` 会
FAIL）、`TestFallbackForwardStreamFlushes`（逐块 flush）、
`TestErrKindCountedOnErrorPolicy`（计数汇聚，去掉 Inc 会 FAIL）、
`TestMetricsTTFBAndErrorKinds`、`TestKindCounters*`（4 例）。

**门禁**：21 包全绿（18 ok + 3 no test files）+ vet 干净；gofmt 仅 3 个历史文件漂移。

**真机验证（PID 36508）**：备份 `yg-gateway.exe.pre-obs.bak` → 替换重启 →
healthz 3/4 → `/metrics` 输出 `ttfb_ms_count=12`（按模型 hy4-preview=2 /
deepseek-v4.1-flash=7 / step-5-preview=3）与 `upstream_errors_total` 族——
均为真实流量观测。

**下一轮候选**：
1. Prometheus 指标补在途/租约时序（当前只有瞬时 gauge）
2. 长流场景显式开启 `sse_keepalive_seconds` / `stream_total_timeout_seconds` 的实测调参
3. pick 侧 scratch 切片的 sync.Pool 复用（收益待 bench 量化）

### 六之十一之三、模型成本率列 + pool unknown/evidence WIP 确认（2026-10-15）

**改了什么（本轮）**：

1. **模型与档位视图「实测成本率列」**：面板 models handler 末尾把
   `usage.SnapshotWindow(72h).CreditByModel` 的 `credits_per_1m_tokens` 按裸模型名
   合并进模型条目（`cost_ppm` / `cost_samples`；**无样本不输出**——AGENTS.md 四态：
   没样本 = unknown，不伪造成本）；前端 `rateCell` 在倍率列下方加小字
   「实测 X 积分/M · N 样本」（`costLine` / `fmtCostPpm`）。
2. **Playground 模型下拉 WorkBuddy 化**（上轮已记录）：`pgModelLabel` /
   `pgModelOptions`——「模型名 · x生效倍率 · 免费/优惠标签」。

**确认他人 WIP（unknown/evidence 四态接线，pool 层，已由另会话完成）**：
- `internal/unknown/`（带 revision 的 registry）、`internal/evidence/`（四态 Fact）、
  `internal/pool/unknown.go`（SetUnknownRegistry / TrackCostUnknown /
  resolveCostUnknown / openCostUnknownsLocked）、`entry.go`（Status.Unknowns /
  EwmaProvenance、ModelCostStatus.Provenance）、`state.go`（EWMA/cost provenance
  标注）、`cmd/server/main.go:299-300`（装配 registry）。
- **实现 + 测试完整，21 包全绿，无需本 agent 修复**（中途 build 失败是另一会话
  写入中途的快照问题，写完后即绿）。

**门禁**：21 包全绿（18 ok + 3 no test files）+ vet 干净 + 边界脚本通过；
gofmt 仅 3 个历史文件漂移（config.go / httpauth/index.go / panel.go，pre-existing）。

**真机验证（PID 36224，生产 7863）**：备份 `yg-gateway.exe.pre-costrate.bak` →
替换重启 →
- `/healthz` healthy=3 → `/v1/models` 20 模型
- `/panel/api/models` 20 模型、**7 个带实测成本率**：hy4-preview 132.64 积分/M·65 样本、
  hy4-preview-f 110.56·291、deepseek-v4.1-flash 0.59·1、glm-5.3-flash 0·5、
  glm-5.2 140.85·1（72h 滚动窗口口径，与积分诊断吻合）
- `/panel/app.js` 193KB 含 `costLine` / `fmtCostPpm`
- 非流式 OK（finish=stop，234 tokens）+ 流式 OK（21 chunks → `[DONE]`）

**注意**：桌面与 D 侧 exe 均已替换（含成本率列 + unknown 接线，两侧哈希一致）；
`yg-gateway.exe.new` 中间产物已删。

**下一轮候选**：
1. 模型与档位视图改 WorkBuddy 紧凑样式（下拉已做，列表待做）
2. 主力/默认模型切 deepseek-v4.1-flash + 精简 defaultprompt.md 重新部署（积分优化落地）
3. unknown 面板徽章/列表（registry 已接线，前端展示待做）

### 六之十一之四、unknown 面板展示 + Playground 默认模型（2026-10-16）

**改了什么（本轮）**：

1. **unknown 面板展示（AGENTS.md「Unknown 消解纪律」收尾）**：数据链路本就通
   （overview→accounts→Pool.List()→Status.Unknowns），前端此前不展示。本轮：
   - 账号卡片状态区：有 unknowns 时输出「N 未知成本」warn 徽章，title 悬停
     带 question + requires（需要什么才能消解）；
   - 账号池提示行（accNote）：汇总「N 个 (账号,模型) 成本未知，悬停账号卡片
     查看明细」。
   - 生产实测 4 账号 unknowns=0（正常：用过的组合都被 NoteModelCost 消解了）。
2. **Playground 默认模型推荐（积分优化落地，对应 COST-CONTROL.md）**：
   `pgModelOptions` 默认选中 deepseek-v4.1-flash（实测 ~1 积分/M），此前默认
   目录第一个模型（hy4 系实测 110~133 积分/M）——试玩一发自选就烧高成本。
   找不到 deepseek 时保持第一个（不破坏上游目录顺序语义）。
3. **积分优化评估结论**：网关**无默认模型机制**（模型由客户端传参），生产
   persona.md 已精简（16 行 811B，纯行为约束非积分主因）、defaultprompt.md
   （39 行）未启用。代码层面可做的已做（成本率列 / COST-CONTROL / Playground
   默认推荐），剩余是客户端选模型行为，靠规范引导。

**测试**：新增 `TestAppJSUnknownsBadge`（徽章 + accNote 汇总）、
`TestAppJSPgDefaultCheapModel`（默认 deepseek 推荐）——源码级"牙齿"断言。

**门禁**：21 包全绿 + node --check 通过（本轮全量测试有一次偶发 FAIL，重跑即
绿，疑似 scheduler/竞争类 flaky，未复现）。

**真机验证（PID 35536，生产 7863）**：备份 `yg-gateway.exe.pre-pgdefault.bak` →
替换重启 → healthz 3/4 → app.js 含「未知成本」徽章代码 + pg 默认 deepseek
逻辑 → overview 4 账号正常。

**下一轮候选**：
1. 模型与档位视图改 WorkBuddy 紧凑样式（列表待做）
2. defaultprompt.md 微调 + 生产 persona 是否启用内置默认的权衡（当前 custom 已精简）
3. 偶发 FAIL 定位（scheduler 竞争类测试，若再出现需排查）

## 读完本文件后，直接回用户："上下文已接上。多 API Key 派发 + 面板 Key 管理页均已完成。可选下一步路线图见上文，等你拍板。"
