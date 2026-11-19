# 真实实例验证：anthropic 错误路径四处修复

日期：2026-11-19
提交：`<待填>`
上一提交：`78cc33e`

## 一、为什么要做这一批

前面几批的 anthropic 修复（`6ab59ea` / `d03b52e` / `addde41` / `1d7a895`）全部由
**fake upstream + httptest** 验证。变更记录里一直挂着同一条未验面：

> 无真实客户端联调证据。所有端到端测试都用 fake 上游 + httptest。

「fake 上游通过」和「真二进制在真 HTTP 上行为正确」是两个结论。这批就是补后者。

## 二、方法：空池实例，零上游调用

**不动生产凭证**（`D:\yg-gateway\auths` 未读写）。改用 e2e 脚本的同一套环境变量机制，
在 `%TEMP%\yg-verify` 建一个沙箱实例：

```
WB2A_LISTEN=127.0.0.1:17865
WB2A_AUTH_DIR=%TEMP%\yg-verify\auths     ← 空目录
WB2A_STATE_FILE=...\data\state.json
```

启动日志确认 `loaded 0 account(s)`。**空池 = 所有请求在进入上游之前就被拒**，
所以本批全程**零上游调用、零积分消耗**。

选择 `:17865` 而非 e2e 的 `:7864` / 生产的 `:7863`，避免串台。

## 三、四条实测（真二进制、真 HTTP）

### 3.1 缺 model → 400（`1d7a895` 的修复）

```
POST /v1/messages  {"max_tokens":64,"messages":[{"role":"user","content":"hi"}]}
=> HTTP 400
{"error":{"code":"invalid_request","message":"transform: model 必填：Anthropic Messages API
 要求 model 字段，空 model 会让网关在不告知的情况下选用上游默认模型","type":"api_error"}}
```

改前实测是 **HTTP 200 + 一条正常回答**（静默降级）。现在被明确挡住。

### 3.2 非法 JSON → 400

```
POST /v1/messages  {"model":
=> HTTP 400
{"error":{"code":"invalid_request","message":"transform: unexpected end of JSON input",...}}
```

### 3.3 合法非流式 + 空池 → 503

```
POST /v1/messages  {"model":"glm-5.2","max_tokens":64,...}
=> HTTP 503
{"error":{"code":"no_healthy_account","gateway_hint":"no healthy account available in pool;
 check /status or retry later","message":"all accounts are temporarily unavailable, please retry later"}}
```

与单测预期一致（单账号池耗尽 → 503 `no_healthy_account`，不是透传上游 429）。

### 3.4 流式 + 空池 → `event: error`（`addde41` + `d03b52e` 的核心）

**这一条最重要**，因为它把记录里的改前/改后实测形成了直接对比。

改前（`addde41` 记录里的原始输出）：

```
event: message_start
event: content_block_start
event: content_block_stop
event: message_delta      stop_reason=end_turn, output_tokens=0
event: message_stop
```

本次实测：

```
event: message_start
data: {"message":{...,"model":"glm-5.2",...},"type":"message_start"}      ← model 回显对

event: content_block_start
data: {"content_block":{"text":"","type":"text"},"index":0,...}

event: error
data: {"error":{"message":"no_healthy_account: all accounts are temporarily unavailable,
 please retry later","type":"upstream_error"},"type":"error"}
```

三点同时成立：
1. **有 `event: error`** —— 失败可见
2. **message 是扁平可读文本**（`no_healthy_account: ...`），不是转义嵌套 JSON —— 客户端不需要二次解析
3. **没有 `content_block_stop` / `message_delta` / `message_stop`** —— 失败后直接 return，不补假收尾

`model` 回显 `glm-5.2` 也顺带验证了 `6ab59ea`（`message_start` 回显请求 model 而非上游默认）。

## 四、这一批消解了什么、没消解什么

**已消解**：上一批记录里「无真实客户端联调」的**一半** —— 现在有真实二进制在真实 HTTP
上按预期输出，不是 httptest 的 fake 世界观。

**仍未消解**（明确不宣称）：
1. **仍未用真实 Anthropic SDK / Claude Code 联调**。它们能否把流内 `error` 事件正确
   弹给用户、能否正确消费 `stop_reason`，仍未验证 —— 需要装 SDK，不属本批范围。
2. **上游真实 error 帧未验**：本批触发的是网关自己的 503，不是上游在流中途发的
   `data: {"error":...}`。后者由单测用 fake upstream 覆盖（`TestAnthropicStreamSurfacesMidStreamError`），
   但**没有真实上游证据** —— 要它有证据必须打真实上游并让它报错，不可控。
3. 空池路径未覆盖「有账号但上游失败」的所有分支。

## 五、清理

实例已停（`port 17865 listening = False`）。stderr 无 panic / FATAL / ERROR，
只有启动日志。产物在 `%TEMP%\yg-verify`（沙箱外，不入仓）。

## 六、回滚

本批无代码改动，仅新增记录。回滚 = `git revert --no-edit <本批 sha>`。
