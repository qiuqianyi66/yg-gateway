# REQ：F-101 面板显式提示（凭证 ACL 自动收紧的面板侧闭环）

> **状态：v1（2026-10-22）。本需求落地 PRD v2 §4.4 债务清单 P0 项「F-101 常态化
> ACL 加固」的面板侧半边：后端写回/启动自动收紧早已落地，缺的是面板零提示。**
> 本文是需求 + 验收 + 判据。落地记录见 `.harness/changes/2026-10-22-f101-panel-notice/`。

---

## 一、必要性（阶段 0，四个问题）

**Q1 要解决的真实问题是什么？**（实测，非"清单上写着"）

PRD v2 §4.4 债务清单 P0 项：「F-101 常态化 ACL 加固」遗留——后端能力已落地但面板
零提示，具体三个缺口（2026-10-22 实测）：

1. **凭证管理区无任何说明**：账号池视图展示几十个号，但用户无从知道凭证文件
   （`auths/*.json` 明文 token）已被自动收紧 ACL 到「仅当前用户 + SYSTEM」。
2. **新增/导入凭证成功后无显式提示**：OAuth 登录成功（`#addDone`）与 JSON 导入
   成功（`#importDone`）只显示「已添加/导入完成」，用户不知道此刻服务已对新文件
   跑过 `icacls /inheritance:r`。
3. **失败兜底路径不可见**：自动收紧失败仅 `log.WARN` 不阻断（`internal/auth/auth.go:375`），
   用户若不看日志，既不知道收紧可能失败，也不知道人工兜底是 `lock-auths.ps1`。

**Q2 不做会怎样？** 不报错，静默缺口。R1（凭证不出 auths/）的唯一人工兜底缺口
继续敞着：收紧失败的机器上，面板照常亮绿灯，明文 token 可能仍对 BUILTIN\Users 可读。

**Q3 现状是不是已经变了？** 变了一半（PRD v2 基线）：
- 部署态已修：`lock-auths.ps1` + 写回后自动收紧（`auth.SaveAtomic → LockdownFile`）
  + 启动兜底扫历史文件（`LoadDirReport`）——后端闭环完整。
- 面板态未动：本需求正是补这半边。**可选的只读状态端点本轮不做**（收紧结果不落盘，
  做端点只能现查 ACL，增益不抵新增面；文案已把失败路径说清楚）。

**Q4 有没有更便宜的等效手段？** 有，且本需求即按最便宜路径做：只加静态文案 + 一个
新 JS 文件（`acl_notice.js`，MutationObserver 挂既有成功提示），**不碰后端、不碰
app.js**（其 4378 非空行已顶 check-depth 基线 maxFrontendLines，一行都不能增）。

**结论：做，L1（前端展示）。** 明确不做：只读 ACL 状态端点、收紧动作本身的任何改动。

---

## 二、实测基线（2026-10-22）

| 事实 | 出处 |
|---|---|
| 写回后自动收紧已落地：`SaveAtomic` → `LockdownFile`（icacls /inheritance:r + grant 服务账户/SYSTEM） | `internal/auth/auth.go:371-378`、`internal/auth/lockdown.go:41-60` |
| 启动兜底扫历史文件已落地：`LoadDirReport` 逐文件 `LockdownFile` | `internal/auth/auth.go:438-443` |
| 收紧失败仅 `log.WARN` 不阻断写回 | `internal/auth/auth.go:375-377` |
| OAuth 添加与 JSON 导入两条路径都走 `SaveAtomic`（即都触发收紧） | `internal/panel/login.go`、`internal/panel/import.go:124` |
| 面板此前零 ACL 提示：index.html 无相关文案、app.js 无相关渲染 | 全仓 grep「收紧/lockdown/icacls」面板侧零命中（除 README/脚本） |

---

## 三、范围边界

**做**
- 账号池视图凭证管理区：静态说明文案（含 `lock-auths.ps1` 人工兜底指引）。
- 「添加账号」弹窗底部：新增/导入凭证落盘后自动收紧的说明。
- `acl_notice.js`：成功提示（`#addDone` / `#importDone`）出现后追加一行显式提示。
- index.go 嵌入 + 路由挂载两个新 JS 静态资源。

**不做（明写）**
- 只读 ACL 状态端点（本轮明确不做；收紧结果不落盘，现查增益低）。
- 收紧动作、icacls 参数、auths/ 目录结构的任何改动（禁止面：`internal/auth/*` 只读）。
- app.js 一行改动（check-depth 基线压线）。
- R5 密钥比较、R10 错误吞咽的任何触碰。

---

## 四、需求项（Given-When-Then 验收）

### F-1 账号池凭证管理区静态说明

- **Given** 面板账号池视图
- **When** 页面渲染完成
- **Then** 凭证管理区展示「凭证文件（auths/*.json）由服务写回后自动收紧 ACL（仅当前用户 + SYSTEM）；收紧失败日志仅记 WARN 不阻断，可手动运行 lock-auths.ps1」

### F-2 添加/导入成功后显式提示

- **Given** 用户完成 OAuth 登录或导入 JSON
- **When** 既有成功提示（`#addDone` / `#importDone`）出现
- **Then** 其下方追加一行「凭证已落盘：写回后服务会自动收紧 ACL（仅当前用户 + SYSTEM）。收紧失败仅记日志（WARN），可手动运行 lock-auths.ps1 复核。」

### F-3 文案纪律（防夸大）

- **Given** 任一提示文案
- **When** 审查文案
- **Then** 不出现「已收紧/保证收紧/已加固完成」类完成态断言——收紧结果面板不可观测，文案只描述机制与失败兜底路径（既有语义：失败仅 WARN 不阻断）。

### F-4 可测证据

- **Given** `go test ./internal/panel`
- **When** 运行
- **Then** 含 R-101 文案断言测试（serve index.html 断言静态说明/弹窗说明/脚本外链存在）+ 新 JS 路由可取 + node --check 语法通过。

---

## 五、验证方式

| 项 | 分级 | 实际验证 |
|---|---|---|
| index.html 静态文案 / 弹窗说明 / 脚本外链 | L1 | `TestIndexHTMLFeatureCopy`（serve /panel/ 断言） |
| 新 JS 路由可下载且文案正确 | L1 | `TestFeatureJSServed` |
| 新 JS 语法 | L1 | `TestFeatureJSSyntax`（node --check，无 node 跳过） |
| 门禁总闸 | — | `scripts/pre-commit.ps1` 全绿；`check-depth.ps1` 无回退（app.js 行数不变，新 JS <1000 行不计入 oversize） |

回滚：纯前端展示 + 两个静态资源挂载，`git checkout` 即可；无二进制/凭证/数据改动。

---

## 六、未完成 / 待补（诚实清单）

- [ ] 浏览器实机冒烟（打开面板走一遍 OAuth 添加）requires：一台可交互 Windows 桌面会话；本轮仅到 DOM 断言级证据。
- [ ] 只读 ACL 状态端点（未来若需要：requires 一个不执行收紧的幂等查询端点设计评审）。
