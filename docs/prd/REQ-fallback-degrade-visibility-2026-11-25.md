---
last_updated: 2026-11-25
status: 已实施
owner: YG
modification_level: L2（对外响应契约）
complexity: A 单文件级
---

# REQ-fallback-degrade-visibility-2026-11-25：备用源降级对客户端可见

**变更类型**：行为/契约变更（客户端可见响应头）。
**一句话**：备用源真正服务请求时，在响应头上标记路由、降级、服务方 host 与上游实际 model；主池成功或网关最终报错时不带这些标记。

## 一、必要性

### 1. 要解决的真实问题

备用上游级联（ADR-015 / R-107）自 2026-10-22 起就在生产路径里：CodeBuddy 账号池全挂时，请求被原样转发到配置的 OpenAI 兼容端点，用量归因到 `realm=fallback`。但**客户端拿到的响应与主池成功完全同形**——同样的 200、同样的 body，唯一的区别只写在网关内部的 usage 台账里。

后果有三条，都是当前可达：

1. **排障方向错误**：客户端/调用方看到模型输出变了、延迟变了，无法判断是主池还是备用源；只能去翻面板/台账，且事后才能确认。
2. **模型降档不可见**：备用源可能按别名或默认模型服务，响应体里的 `model` 与请求模型不一致；客户端拿不到「实际是谁、实际哪个模型」的信号。
3. **「成功」的语义被稀释**：对上层来说，fallback 的 200 与主池 200 不可区分，等于把一次路由切换当成没发生。降级是设计内的能力，但**不可见**会把它变成静默事实。

### 2. 不做会怎样

- 后果可见性：低（不报错、不告警）。
- 严重度：中高（排障与成本归因被误导；模型降档被当成主模型输出）。
- 触发面：任何启用 fallback 的部署，且主池耗尽/全挂时必然触发。

### 3. 现状是否已变

已变一半：2026-11-21 已在工作区补了响应头（未提交），但复审发现一个真实缺陷——三个标记在「拿到上游响应」时就写进 `ResponseWriter`，而非流式读体失败会返回 false 继续级联/走末端错误，**残留的 `X-YG-Served-By` 会把网关错误伪装成「备用源已服务」**。本轮把标记挪到真正写出响应头的那一刻，并补测试钉住。

### 4. 有没有更便宜的等效手段

- 只写日志：客户端拿不到，不满足「调用方可判别」。
- 面板/`/status` 聚合：只能事后看，不是 per-response。
- 响应体加字段：改变 OpenAI 兼容 wire 形状，严格客户端可能拒绝解析。
- 新增配置开关：多一个状态组合，收益不增。

结论：**做**。响应头是零 body 变更、零新依赖、零新配置的 per-response 通道；沿用现有 `X-YG-` 命名空间。

## 二、范围

### 做什么

1. `forwardToOne` 在**真正写响应头前**设置三个标记：`X-YG-Route`、`X-YG-Degraded`、`X-YG-Served-By`。
2. 非流式在完整读体并解析 `model` 后，额外设置 `X-YG-Upstream-Model`；流式不解析 body，该头缺省（unknown）。
3. `ServeHTTP` 的 CORS 响应增加 `Access-Control-Expose-Headers`，列出四个头，浏览器跨域客户端才读得到。
4. 所有备用源都失败、最终走网关错误时，不带任何 `X-YG-*` 标记。
5. README 口径改为「带 = 备用源；没有 = 未标记（不能反推主池）」。

### 不做什么

- 不改响应体、不加 `route` 字段（OpenAI 兼容优先）。
- 不改 reqlog/usage 既有 `realm=fallback` 归因（那是聚合事实，不是 per-response 标记）。
- 不改上游请求头/协议（R8）。
- 不做面板可视化、告警或配置开关（本项只解决「客户端可见」）。
- 不把 `X-YG-Served-By` 扩成完整 URL（路径/查询/凭证不出网关）。

## 三、契约

| 响应头 | 何时出现 | 值 | 缺省语义 |
|---|---|---|---|
| `X-YG-Route` | 备用源响应真正写出时（流式在写流前，非流式在读完 body 后） | `fallback` | 未标记（可能是主池，也可能是中间层剥离，**不是**「一定主池」） |
| `X-YG-Degraded` | 同上 | `1` | 未标记 |
| `X-YG-Served-By` | 同上 | 备用源 `host[:port]`；URL 不可解析时为 `fallback-endpoint` | 未标记 |
| `X-YG-Upstream-Model` | 仅非流式：响应体 `model` 非空且可解析 | 响应体里的 model 原文 | 未知（流式不解析 body；缺失/解析失败同样未知） |
| `Access-Control-Expose-Headers` | 所有 API 响应 | 上述四个头名 | 浏览器跨域读不到非安全头；缺省时按浏览器 CORS 规则 |

边界：

- 备用源返回 4xx/5xx 但 body 完整：仍算「备用源服务」，标记存在，状态码原样透传。
- 备用源连接失败、非流式读体失败、流式中途断开：不写标记；流式已写出的部分保留已发字节（不可撤回）。
- 主池成功：不经过 `fallbackForward`，标记不出现。
- 头名固定 `X-YG-*`；`Served-By` 只暴露 host，不含 userinfo/path/query/fragment。

## 四、验收（GWT）

- **G1** 非流式成功：`Route=fallback`、`Degraded=1`、`Served-By` 含备用源 host、`Upstream-Model` 等于响应体 model。
- **G2** 流式成功：前三个头在写流前可见；`Upstream-Model` 为空（unknown）；SSE body 原样透传。
- **G3** 全部端点失败：不残留任何 `X-YG-*`（牙齿：把标记移回「拿到 resp 后」→ `TestFallbackFailureDoesNotLeakHeaders` FAIL；实测旧实现红在 `X-YG-Route="fallback"`）。
- **G4** `Served-By` 只含 host：带 userinfo/path/query 的 URL 只取 host；非法 URL 回落 `fallback-endpoint`。
- **G5** CORS 预检 `Access-Control-Expose-Headers` 精确列出四个头（牙齿：删掉该行 → `TestCORSPreflightHTTPSOriginReflected` FAIL）。
- **G6** 既有 fallback 语义不回退：级联顺序、模型白名单、4xx 透传、半截 SSE 判失败（既有测试全绿）。

## 五、回滚

`git revert <本提交>` 即可：删掉响应头与 CORS 暴露行，无配置迁移、无数据写入。客户端对未知响应头天然忽略，回滚无兼容风险。

## 六、观测与边界

- 聚合事实仍以 usage `realm=fallback` 为准；响应头是 per-request 观察。
- 中间代理可能剥离自定义头，故**头的缺省是 unknown，不是「主池」的否定证据**（四态纪律）。
- L2 要求的独立复审**待**另一只读会话执行；本轮自评审记录见 `.harness/changes/2026-11-25-fallback-degrade-visibility/review.md`。
