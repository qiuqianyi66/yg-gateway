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

## 三、构建与测试（关键环境）

```powershell
# Go 工具链在临时目录（不是系统 PATH 里的 go）
$go = "$env:TEMP\go\bin\go.exe"          # Go 1.27.2
$env:GOFLAGS = '-mod=mod'
$env:GOTOOLCHAIN = 'local'
$env:GOPROXY = 'https://goproxy.cn,direct'   # proxy.golang.org 本机不通
$env:CGO_ENABLED = '0'

& $go build -trimpath -ldflags="-s -w" -o yg-gateway.exe ./cmd/server
& $go vet ./...
& $go test ./...                          # 19 包，全绿
node --check internal/panel/app.js        # 前端语法（go:embed 不查内容）
```

- 全量测试：15 个有测试的包 ok + 4 个 no test files = 19 包。
- 部署踩坑：**构建+swap+启动必须放同一条命令**（跨命令间新 exe 会被实时防护删除）。

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

## 五、当前待办（新对话从这里接）

**主线已完成**：
- ✅ 多 API Key 派发（一期，ADR-001）：httpauth 前缀索引 + 常量时间比较、realm/models 约束、热更新、脱敏回显
- ✅ 面板 Key 管理页（二期，ADR-002）：卡片+弹窗、新增/编辑/启停/删除/一键生成、明文一次性展示
- ✅ 用量按 Key 聚合（ADR-001 F-005 二期收尾）：`by_key` / `credit_by_key` 两维度 + 面板「按 Key」页签；usage.json 升至 v5，旧数据无损归入「未归因」
- ✅ `/v1/models` 档位字段双名兼容（`supported_efforts` / `default_effort` 别名，兼容只认裸名的客户端）
- ✅ 全仓 gofmt 归零（18 个文件的历史对齐漂移）

**可选下一步路线图**（等你拍板）：
1. OpenRouter 费率折算（回答"我省了多少钱"）
2. 增量用量聚合 + 落库（目前 JSONL + usage.json，量大后查询会慢）
3. 自动养号任务补齐（任务中心有 Sequential_Tasks_* 预留）
4. 更完整的客户端生态（Claude Code / Codex 直连）
5. Anthropic 协议双支持

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
