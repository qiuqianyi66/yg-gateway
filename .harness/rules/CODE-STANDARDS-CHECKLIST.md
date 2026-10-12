# 主要代码规范清单（速查）

> 从 `.harness/rules/` 三份规范（编码规范 / 开发流程规范 / 工程结构）提炼的**可操作清单**。
> 每次改动前后对照自检。完整释义见原文件，本文只做速查卡。
>
> 更新：2026-10-12

---

## 一、红线 15 条（🔴 零违反，评审不可降级）

| # | 红线 | 一句话判据 |
|---|---|---|
| R1 | 凭证不出 `auths/` | accessToken/refreshToken 禁入日志、`state.json`、归档 JSONL |
| R2 | 日志脱敏 | 禁含 Authorization 头、提示词正文、响应正文；UID 只取前 8 位 |
| R3 | 无裸 `innerHTML` 拼用户输入 | 变量插 HTML 前必过 `esc()`；**属性插值尤甚**（引号可闭合属性注入事件处理器） |
| R4 | 路径穿越防护 | 用户输入拼路径处必须白名单（UID `[A-Za-z0-9_-]` ≤64） |
| R5 | 密钥常量时间比较 | 鉴权一律走 `internal/httpauth`，**禁 `==`** 比较密钥 |
| R6 | 出站请求集中 `upstream` | 禁其他包直接 `http.Post` 到上游；出站改写走统一管线 |
| R7 | 凭证读写加锁 | 经 `auth` 的 `AccessTokenValue()`/`RefreshTokenValue()`，禁直读字段 |
| R8 | 上游协议数据不得改名 | `wb2api-<ms>` conversationId、`chatcmpl-wb2api` 哨兵属报文内容，改名有风控风险 |
| R9 | goroutine 有回收路径 | 起 goroutine 必有 ctx 取消或明确退出条件，禁裸 `go func()` |
| R10 | 错误不静默吞 | `err != nil` 必处理或显式注释为何忽略；禁 `_ =` 吞业务错误 |
| R11 | 配置热生效标注清楚 | 改配置结构时明确「热生效」还是「需重启」，面板保存后提示对应文案 |
| R12 | 定时器不泄漏 | `Ticker`/`Timer` 必 `Stop()`；长生命周期循环配 ctx |
| R13 | 前端改必重编译 | 改 `app.js`/`index.html` 后**必须 `go build`**（go:embed），否则线上不生效 |
| R14 | 构建工具链 ≥ Go 1.27.2 | 更低版本带 stdlib 漏洞（HTTP/2 + TLS），禁用于产出二进制 |
| R15 | 二进制替换前备份 | 部署前保留旧版（`.v{版本}`），保证可回退 |

---

## 二、命名

| 对象 | 约定 | 例 |
|---|---|---|
| 包 | 小写单词，禁下划线 | `httpauth`、`redisstore` |
| 文件 | 小写，多词下划线 | `credit_floor_test.go` |
| 导出标识符 | PascalCase | `VerifyBearer` |
| 未导出 | camelCase | `creditsEarliestExpiry` |
| 常量组 | 语义前缀 | `bindPrefix`、`stateKey` |
| 接口 | 单方法用 `-er` | `Store`、`Closer` |
| 前端变量 | camelCase；常量 UPPER_SNAKE | `EXP_FRESH_MS` |

- 包名禁与标准库冲突；`internal/` 下包名即目录名。
- 前端 DOM id 用语义名（`expBox`/`btnExp`），禁 `div1`。

---

## 三、错误处理

```go
// 🟢 正确：%w 包装保留上下文
return fmt.Errorf("refresh token uid=%s: %w", a.UID, err)
// 🔴 错误：吞掉或丢上下文
_ = c.RefreshToken(a)
```

- 用 `%w`（保留 `errors.Is/As`），禁 `%v` 拼接丢链。
- 业务错误分类集中：上游错误一律经 `Classify`（`internal/upstream`），禁各处散落状态码判断。
- 用户可见错误走 `writeErr(w, status, msg)`，**禁把内部错误原文直接回客户端**。

---

## 四、并发

- 共享状态明确锁归属，注释写明为什么（如 `auth` 的 `mu` 保护 token）。
- **持锁期间不做 IO**（网络/磁盘），只做内存读写。
- 写回前重校验快照一致性（防并发刷新覆盖），见 `client.go` 的 `atBefore/rtSnapshot` 模式。
- 禁持锁调用会反向加锁的方法（死锁），见 `auth.Realm()` 注释。

---

## 五、安全（头等大事）

| 面 | 约束 |
|---|---|
| 凭证落盘 | `0o600` + 原子写（tmp+rename）；**Windows 另需 `lock-auths.ps1` 改 ACL**（Go 0o600 不剥继承 ACL） |
| 日志 | 归档 JSONL 只存元数据；来源 IP/UA 由 `logging.request_client_info` 控制，可关 |
| 鉴权 | `api_key` 空 = 不鉴权；非空时面板与 API 同口径；**普通下游 Key 不给面板权限** |
| 响应头 | 面板与 `/panel/api/*` 统一带 CSP / nosniff / DENY / no-referrer / COOP / CORP |
| XSS | `esc()` 转义 5 字符（`& < > " '`）；新渲染函数必配 payload 注入测试 |
| 路径穿越 | UID 白名单 + `filepath.Join` 后校验前缀 |
| CSP | `script-src 'self'`：**禁内联脚本、禁内联事件处理器**（`on*=` 属性） |

---

## 六、前端（内嵌面板）

- 🔴 无内联脚本、无内联事件处理器（CSP 要求）。**事件一律 JS 动态绑定**（`addEventListener` / `.onclick=`）。
  - 历史坑：`onsubmit="return false"` 被 CSP 拦截 → 回车提交整页刷新 → 密钥丢失 → 401 循环。已修（`keyGateForm` + `preventDefault`）。
- 🔴 所有动态文本走 `esc()`；`innerHTML` 只用于静态模板 + 已转义插值。
- 🟢 新增渲染函数 → 配一个"有牙齿"的结构测试（还原 bug 必须 FAIL）。
- 🟢 数据缓存与上游调用分离：**打开视图不打上游**（逐账号实时查会卡死面板），显式按钮才触发。
- 🟢 设计令牌走 CSS 变量（`:root` / `[data-theme]`），禁裸色值散落。

---

## 七、测试

- 表驱动为主（见 `internal/pool` 各 `_test.go`）。
- **测试必须有牙齿**：新增修复类测试需验证"还原 bug 后 FAIL"。
- 前端测试用 `node` + `vm` 跑代码切片（见 `frontend_test.go`），无 node 时 `t.Skip`。
- 涉及上游协议的测试用 `httptest.Server` mock，**禁打真实上游**。

---

## 八、流程（十阶段）

| 阶段 | Gate |
|---|---|
| 1 需求分析 | 需求文档产出**且用户确认**（范围边界 / F 编号 / Given-When-Then / 非功能约束 / 影响分析） |
| 2 方案设计 | 方案经用户确认（D-编号决策拍板），红线冲突清零 |
| 3 任务拆分 | 粒度 ≤4h，依赖明确 |
| 4 编码 | 红线零违反，`go build ./...` 0 error；**40% 分批，批批验证** |
| 5 单测 | 核心逻辑覆盖（含牙齿验证），全绿 |
| 6 评审 | 6 维评审无 🔴；🟡 清零才过 |
| 7 集成 | 端到端链路通过 |
| 8 预发 | 独立端口/目录验证 + 冒烟 |
| 9 部署 | 二进制替换 + 备份 + 重启，冒烟通过 |
| 10 观测 | 观测期无异常，指标在基线内 |

**路径分级**：L0 文档 / L1 前端展示（轻量，阶段 1-3 合并）/ L2 业务逻辑（全流程）/ L3 架构（全流程 + ADR）。
🔴 阶段 N 的 Gate 未满足，**禁止**进入 N+1。🔴 阶段 1 需求未经用户确认，禁止进阶段 2。

---

## 九、工程结构边界

- 🔴 禁提交：`auths/`、`config.json`、`data/`、`*.exe`。只读 `config.example.json`。
- 🟢 `upstream` 是**唯一出站口**；`auth` 是**凭证唯一持有者**。
- 🔴 `internal/*` 不得循环依赖（当前 DAG）；只有 `cmd/*` 可有 `func main()`。
- 🟢 每次改动建 `.harness/changes/{feat-name}/`（`summary.md` + `review.md`），分支同名 `feature/{feat-name}`。
- 🟢 文档分层：先 `docs/prd/` 后 `docs/plans/`；ADR 三条件全满足才写。

---

## 十、提交前自检（精简 26 项）

**通用**：`go build` 过 · `go vet` 无输出 · `go test ./...` 全绿 · `gofmt -l` 无改动文件 · 新代码有测试且验过牙齿

**前端**：`node --check app.js` 过 · 改 JS/HTML 已重新编译 · 所有输入渲染过 `esc()` · 无内联脚本/事件处理器

**安全**：无凭证/密钥进日志或代码 · 无裸 `innerHTML` · 路径拼接有白名单 · 密钥比较走 `httpauth` · 依赖变动跑 `govulncheck`

**部署**：二进制替换前已备份 · 配置字段标注热生效/需重启 · 变更已写 `.harness/changes/{feat}/summary.md` · 端到端冒烟通过

---

## 十一、Commit 规范

- `type(scope): message`，type ∈ feat/fix/refactor/test/docs/chore。
- subject ≤72 字符，body 讲 **why**。
- 缺陷修复与功能/测试**分开提交**。
- 破坏性改动走 expand-contract（先并存 → 分批迁移 → 删旧）。
