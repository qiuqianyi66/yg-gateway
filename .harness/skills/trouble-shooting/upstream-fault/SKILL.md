---
name: upstream-fault
description: 上游故障排查——WAF 403 / 风控 / 半死连接 / SSE 断流 / 模型 404 等网关转发故障的定位流程。当确认是"打上游"出问题时触发。
---

# 上游故障排查（upstream-fault）

## 触发条件

- 已确认故障在上游侧（本地网关 healthz 正常，但 chat/models 打上游报错）
- 症状：WAF 403 / 风控 / 11101 参数错误 / 11102 模型不存在 / 11133 不支持图片 /
  6004 限额 / 半死连接卡 60s / SSE 流中断

## 故障分层（按概率从高到低）

### 1. 账号态（最常见）

- **session 失效**：token 过期/被踢 → `disabled`，需重新 OAuth 登录
- **积分耗尽**：credit 触底，收费模型 503 / credit_floor 拦截 → 查 `/status` 的
  `credits` + `credit_floor`
- **企业版账号**：growth 系统 403（签到/旅行/任务全 403），chat 仍可用

排查命令：
```
Invoke-WebRequest http://127.0.0.1:7863/status -Headers @{Authorization="Bearer $k"}
# 看 accounts[].state（healthy/cooling/disabled）、credits、model_locks
```

### 2. 冷却/熔断态

- **软冷却**：429/限流文案 → 指数退避（soft_rate 600s 起）
- **硬冷却**：402 → 次日 04:00
- **熔断**：连续失败达阈值 → 30m 起指数退避
- **404**：固定 60s（上游偶发路径缺失，不参与指数退避）

排查：`/status` 的 `cooling` 计数 + 面板「账号池」视图的冷却截止时间。

### 3. 连接层（半死连接）

症状：偶发卡 60s、`http2: timeout awaiting response headers`。

- 查 `transport.go`：TLSNextProto 必须**空 map**（非 nil）才真禁 h2
- keepalive 15s、Dial 10s、TLS 握手 10s、ResponseHeaderTimeout 60s
- 失败后 `roundTripCloseIdle` 清空闲池（半死连接不残留）

### 4. 报文形态（R8 敏感区）

症状：上游突然 400/403/风控，而之前正常。

- **先查是不是报文被改了**：conversationId / 哨兵 id / UA 指纹 / X-IDE-* 归因头
- 用 scripts/ 诊断脚本抓真实报文对比（probe_active.py / probe_max_tokens.py）
- **R8 铁律**：协议数据不得改名，改名有风控风险，必须先评估 + 留回滚

### 5. 模型目录漂移

症状：客户端选到不存在的模型 → 11102。

- `/v1/models` 缓存 10min，负缓存 5min；目录新增模型最多滞后 10min
- 面板模型页实时，API 缓存——两套口径漂移是已知（PR #38）

## 排查黄金流程

1. `/status` 看账号态（state/credits/cooling）—— 定位到账号还是网关
2. 面板「运行日志」看 `[upstream]` 标签的 WARN/ERR 行（有分类，如 `bad_params`/`rate_limited`）
3. 确认报文没被改（R8）
4. 真实账号端到端冒烟（httptest mock 只能测结构，不能测风控）

## 纪律

- 所有日志/捕获物脱敏：token、prompt 正文、响应正文都不进排查记录
- UID 只取前 8 位（logfmt.Label 已做）
- 不把内部错误原文直接回给客户端（走 writeErr 分类）

## 自检

- [ ] 已定位到账号态/冷却/连接/报文/目录 哪一层
- [ ] R8 报文未被误改
- [ ] 排查记录脱敏
