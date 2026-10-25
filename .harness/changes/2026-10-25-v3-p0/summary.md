# 2026-10-25 v3-p0（ROADMAP v3 P0 前两项：count_tokens + key 用量告警）

## 背景与必要性

按 ROADMAP-prd-v3 P0 分期执行（V3-1/V3-2；V3-3 等生产机换版、V3-4 在用户手里）。
两件都是对标+实测坐实的缺口：count_tokens 是 Claude Code 原生调用（CRS/LiteLLM/
magpie 三家原生支持，grep 本仓 0 命中）；key 用量告警补「多 Key 派发下消耗突增
不可见」（usage 按月按 Key 账已有，缺告警桥接）。

## 改了什么

**V3-1 count_tokens**：
- `internal/server/count_tokens.go`（新）：`POST /v1/messages/count_tokens`，
  不引分词依赖——三段（system/messages/tools）解析为通用值后**只对字符串叶子**
  启发式估（CJK 全角 ≥0x2E80 计 1、其余 1/4 向上取整；role 值计入=计费口径）。
  响应 `{input_tokens, estimated:true}`（四态诚实口径：不伪装上游精确值）。
  body 上限与聊天同源（MaxBodyBytes→413）；整体 JSON 非法 400；子段畸形宽容。
- 路由注册（withAuth 同源）；测试 ×5（数值锚定/工具段/宽容/413/诚实标注）。

**V3-2 key 用量告警（无配置项零回归）**：
- `internal/alerting/notifier.go`：+`NotifyKeyUsage`（AccountUID 承载 keyID——
  send 去重键 Type:AccountUID 天然按 key 去重）。
- `cmd/server/keyalert_wiring.go`（新）：5 分钟周期查 `MonthlyUsed`，阈值
  `api_keys[].usage_alert_tokens`（月 token 口径），每 (月,key) 闩锁一次、
  月滚动自动重新武装；无配置项不起 goroutine；ctx 退出（R9）。
- config.go：APIKeyConfig +UsageAlertTokens（压缩 MonthlyTokenQuota/AllowedCIDRs
  注释腾行，1089=基线）；config.example.json 补示例值。
- main.go：startKeyUsageAlerts 接线（压缩倍率表注释腾行，1000 恰在 >1000 线下）。

## 怎么验证的

三包定向绿（server 9s / cmd/server / alerting）；新增测试 8 个，其中两处第一版
断言被实测修正（整串估被信封高估 8% → 改字符串叶子口径并更新锚定注释；k2 新月
爆表应告警——期望值笔误），均有「数值/行为锚定」留下。全量 pre-commit 通过
（含棘轮：config.go 1089=基线、main.go 1000 恰在线下）。

## 怎么回滚

单提交 revert；两件都默认零行为变化（count_tokens 是新增端点；告警需显式配置
usage_alert_tokens>0）。

## 遗留

- count_tokens 权重是 derivation：V3-3 真机校准（±30% GWT 条款）后收紧。
- main.go 已到 1000 线贴脸——V3-5 config 表驱动时一并给 main 减负（ROADMAP 已排）。
