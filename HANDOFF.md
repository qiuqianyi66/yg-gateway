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

**⚠️ 运行实例现状（2026-10-12 实测）**：
- **唯一在跑的是 `D:\yg-gateway-eval\yg-gateway.exe`，监听 127.0.0.1:7864**（4 号健康，
  面板 `http://127.0.0.1:7864/panel/`）。它的 config 指向 `auth_dir: D:/yg-gateway/auths`。
- `D:\yg-gateway`（7863）**当前未运行**（上一轮收尾时还在跑，之后被停止，原因未记录）。
  如需恢复：§三的构建+复制+启动同命令模板把目标路径/端口/`--config` 换掉即可。
- 两边 config 是**独立的**：改一边不会自动同步到另一边（见 §五已应用配置清单）。

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

**已应用的运行配置**（D:\yg-gateway\config.json，注意：该实例当前未运行，见 §二）：
- `pool.global_max_in_flight = 18`（4 账号 × per-account 4，留余量）
- `pool.breaker_probe_gap` 空 = 默认 30s
- `session_sticky.derive = false`（关掉内容派生，修「只用一个号」）

**已应用的运行配置**（D:\yg-gateway-eval\config.json，**当前活实例**，7864 端口）：
- `session_sticky.derive` **未设置**（= 默认开；上一轮对「本实例」设的 false 落在
  D:\yg-gateway\config.json，两边配置并未同步，若需关闭派生记得同步到这里）
- `pool.ewma = {enabled:true, alpha:0.1, min_samples:5}`（ADR-005 真机验证，保守参数）

**下一轮候选**：
1. 粘性路径的成本分层（ADR-004 §五 方案 D 被弃：与缓存复用冲突，需重新设计）
2. 前端剩余：表头排序、请求详情抽屉、通知中心、命令面板、批量选择、虚拟滚动
3. 竞品路线图：OpenRouter 费率折算 / 增量用量落库 / 客户端生态 / Anthropic 协议
4. 可选打磨：EWMA 参数调优观测（/status 已透出信号）、pick CAS-claim（当前由
   Acquire+tried 兜底，仅同 tick 并发场景才需考虑）

**⚠️ 环境坑（已踩，务必注意）**：
- **写 `D:\yg-gateway\config.json` 不要用 PowerShell `Set-Content -Encoding UTF8`**
  —— 会加 UTF-8 BOM，Go 的 json 解析直接失败（`invalid character '\ufeff'`），服务起不来。
  用无 BOM 写法：
  ```powershell
  $enc = New-Object System.Text.UTF8Encoding($false)
  [System.IO.File]::WriteAllText($path, $text, $enc)
  ```
  更稳：走面板 `POST /panel/api/config`（服务端自己落盘，格式天然正确）。
- **火绒会删新建的 exe**：构建 + 复制 + 启动必须放同一条命令；或加白名单（详见 §三）。

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
