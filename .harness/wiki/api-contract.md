---
last_updated: 2026-10-20
status: active
owner: YG
---

# 接口契约（YG Gateway）

> 本文件写**契约语义**（鉴权口径 / 限流 / 错误信封 / 字段四态 / 受保护字符串），
> **不复制路由表**。路由唯一真相在代码：`internal/server/handler.go` 的 `NewHandler`（外层 mux）
> 与 `internal/panel/panel.go` 的 `routes()`（面板 mux）。
>
> 为什么不列路由：抄一份必然漂移。本项目已实证三次同类漂移——LESSONS L-1/L-3 的「检查 N/M」
> 编号、两个 changes 目录漏 summary.md、`pre-commit.ps1` 检查 9 自己的描述。
> 路由这种事无巨细的清单，漂移只是时间问题；要查路由读代码，那才是单一写点。
>
> 配套：`glossary.md`（领域术语）、`data-model.md`（落盘布局与不变量）。

## 一、两个 mux 与挂载方式

| mux | 位置 | 挂载 |
|---|---|---|
| 外层（网关 API） | `internal/server/handler.go` `NewHandler` | 进程主 mux |
| 面板 | `internal/panel/panel.go` `routes()` | `h.mux.Handle("/panel/", cfg.Panel)`，pattern 自带 `/panel` 前缀，**外层不做前缀剥离** |

- `/panel` → `/panel/` 由 Go ServeMux 自动重定向。
- 面板 HTML（`GET /panel/{$}`）与 `app.js` **匿名可加载**（页面本身无秘密）；
  数据接口 `/panel/api/*` 才走鉴权。
- `/panel/` 前缀已注册，其内部自行处理 404，**不会落**到外层兜底。

## 二、鉴权口径：两个 `withAuth` 不同口径（承重墙）

| | `server.withAuth` | `panel.withAuth` |
|---|---|---|
| 认什么 | `api_keys` 全部条目 + 顶层 `api_key` | **只认单 `api_key`（管理员）** |
| 给谁用 | 下游客户端调 `/v1/*` | 浏览器里的账号管理 / 配置读写 |
| 出处 | ADR-001 | ADR-001 阻断项 3 |

🔴 **这是一条承重墙**：把面板口径放宽到 `api_keys`，等于把账号管理与配置读写开放给所有下游 Key。
改任何一边前先读 ADR-001 的阻断项清单。

密钥比较走 `internal/httpauth`（常量时间，禁 `==`，R5）；面板侧经 `livecfg` 快照读取——
面板里改了 `api_key`，**下一个请求即用新值**，无需重启。

## 三、限流与免鉴权

- `rateLimited` 只包**只读**端点：`GET /v1/models`、`GET /status`、`GET /metrics`。
- `GET /healthz` **免鉴权也免限流**（探活用）。
- 写端点（`POST /v1/chat/completions`、`POST /v1/messages`）不走只读限流。
- `/metrics` 与 `/status` 同鉴权口径：指标含账号池状态与 token 量，属敏感运维数据，
  不对未鉴权方开放。
- `read_only_rate_limit.limit = 0` 的语义（关闭 vs  unlimited）**未核实**，用前先读
  `internal/server` 的限流装配代码。

## 四、错误信封

统一 `{"error":{"message","type","code"}}`，并区分 **404 / 405**。

为什么需要：Go 1.22 ServeMux 对未注册路径回裸 `text/plain "404 page not found"`、
对已知路径的错误方法回 `405 Method Not Allowed`，OpenAI 兼容客户端（Codex / Cherry Studio /
各类 SDK）解析这类响应会失败，报出的错误与真实原因无关
（`invalid character 'p' looking for beginning of value`），排查成本高。

405 判定用**显式已知路由表**（路径命中已知路由且方法不符 → 405，否则 404），
不探测 mux 内部状态——显式表比探测更稳。该表与路由注册是同源的两份，改路由时**两处都要改**
（这是本文件不列路由的原因之一：列了就成了第三份）。

## 五、响应字段的四态：缺失 ≠ 否定

契约消费方必须按**三值**处理：`true` / `false` / **字段缺失 = unknown**。
**禁止**用 `!field` 判否。

完整清单以 `AGENTS.md`「事实语义纪律」为**唯一真相**（单一写点，此处不复制）。
本文件只记契约层面的后果：

- `/v1/models` 的 `supported_efforts` / `supports_images` 缺失 → 客户端应显示「未知」，
  **不得**显示「不支持」。精简目录未返回 ≠ 能力不存在。
- `ewma_success` / `ewma_latency_ms` 样本不足时服务端**不输出数字**（不是 0）。
  消费方读到缺失应显示「样本不足」，不得当 0 参与排序。
- `model_costs` 的 tier 是 **inference**（少量 credit 样本外推），不是观测到的事实。
- `/status` 的 latency 分位数是 **derivation**（固定分桶**插值**估算，非精确值）。

## 六、R8 保护的报文字符串（不得改名）

以下属**报文内容**，改名有风控风险，任何「优化」一律拒绝：

- `wb2api-<ms>` 形式的 conversationId
- `chatcmpl-wb2api` 哨兵 id
- 三段式 UA 指纹（`WorkBuddy/<ver> ... CLI/<cliVer>`）

唯一真相在 `AGENTS.md` Boundary Contracts R8，影响面 `internal/upstream/headers.go` /
`client.go` / `useragent*`。此处只做指针，不复制。

## 七、流式

- 出站**强制 stream`（见 glossary「出站改写管线」）。
- `POST /v1/chat/completions` 与 `POST /v1/messages` 两条入口都走同一条出站管线。
- 客户端传 `stream:false` 时网关侧如何聚合/何时回非流式响应：**未核实**，
  读 `internal/server/chatrun.go` / `chatflow.go` 后再下结论。

## 八、面板 API 的共性

- 安全响应头由 `Panel.ServeHTTP` **统一**写（CSP 限制脚本只能来自本服务），
  页面 / 静态资源 / API / 401 错误响应全都带——API 也可能被浏览器直接打开。
- 全部 `/panel/api/*` 走 Bearer 鉴权（口径见 §二）。
- 面板前端 `internal/panel/app.js` 是 `go:embed` 进二进制的单文件，改它要**重新编译**
  才生效（不是 `git checkout` 就完事）。
