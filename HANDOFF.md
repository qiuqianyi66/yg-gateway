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

## 读完本文件后，直接回用户："上下文已接上。多 API Key 派发 + 面板 Key 管理页均已完成。可选下一步路线图见上文，等你拍板。"
