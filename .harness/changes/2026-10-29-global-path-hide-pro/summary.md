# 2026-10-29 global 路径修复 + Pro 模型隐藏 + 11128 重分类

## 改了什么

1. **global chat 路径改回 `/console/chat/completions`**（commit `78156f0`）
   - 之前固定走 `/v2/chat/completions`，国际版上游现在只在 `/console` 收对话，
     `/v2` 一律 400 code=11128 "request illegal"。
   - `chatPaths()` 改为 global 返回 `["/console/chat/completions", "/v2/chat/completions"]`，
     fallback 只在 404/405 时触发（其他 4xx 不换路径，避免误熔断）。
   - R8 golden 测试用 `-update-r8-golden` 重写。

2. **过滤路由别名 + enterprise 路兜底**（commits `487dc4e`/`dab1ef2`/`306d24a`）
   - `nonChatModel()` 加 `routingAliasIDs` 黑名单：default-model/auto/fast-model/
     balanced-model/primary-model/deep-model/auto-chat/enhance-1.0/cn:auto/
     deepseek-v4.1-flash-sg。
   - enterprise 探测路（`/v2/enterprises/personal/models`）不过滤会绕回别名，
     在 `global_models.go` 落缓存前加统一兜底过滤。

3. **/v1/models name 字段拼倍率后缀**（commit `019597c`）
   - 之前只把倍率拼在 `id` 字段，`name` 字段只有裸名——DSH 类客户端读 name 显示裸名。
   - `applyModelInfoFields()` 同时拼进 `name`。

4. **隐藏 9 个当前号池无权限的 global 模型**（commit `4ca5d9c`）
   - `models_handler.go` 加 `hiddenGlobalListingIDs` map：gpt-6-astra、gpt-5.6-sol、
     gpt-6-sol、gpt-6.1-sol、kimi-k3、kimi-k3.1-pro、kimi-k3.1-pro-highspeed、
     minimax-m3.1-flash、o4-mini。
   - 默认列表跳过，手动指定 `model=global:<id>` 仍可路由（开源用户升级 Pro 后
     从 map 删 id 即恢复可见）。

5. **11128 按业务码重分类为 bad_params**（commit `19a81a6`）
   - 之前 11128 的 msg 也是 "request illegal"，被 `accountFaultMarkers` 子串匹配
     误归 `ErrAccountFault`（熔断账号）。但 11128 是请求形态问题（路径错/首条非 system），
     换任何号都复现，不该熔断。
   - 在 `accountFaultMarkers` 判定前加 `hasBusinessCode(body, "11128")` 特判 →
     `ErrBadParams`（不罚号、不轮转、透传原文）。
   - 例外：body 同时带 contentBlocked 文案时仍归 `ErrContentBlocked`。

## 怎么验证

- 全模型探针：CN 17/17 通，global 17/26 通（9 个 Pro 无权限已隐藏）。
- `/v1/models` 从 43 → 34（CN 17 + global 17）。
- `global:gpt-5.5` 端到端：HTTP 200，回复 "Hi! How can I help you today?"。
- `go test ./internal/upstream/` TestClassify 通过（含 11128 + content_blocked 用例）。
- 生产部署脚本冒烟三条全过（/healthz、非流式、流式）。

## 怎么回滚

- 二进制备份：`D:\yg-gateway\yg-gateway.exe.pre-fix-11128-classify-20261029.bak`
  （回退到隐藏 Pro 模型后、11128 重分类前）。
- 更早备份：`yg-gateway.exe.pre-hide-pro-models-20261029.bak`（回退到 global 路径修复后）。
- 手动回滚：停生产 → 备份 exe 重命名回来 → 启动。

## 未消解的 unknown

- minimax-m3.1-flash / o4-mini 在 CN 域通但 global 域 503：不一定是 Pro Only，
  可能是 global 号没这俩模型权限/模型 global 侧下线——未实测确认（当前已隐藏，不影响）。
- DSH 客户端本地缓存了旧模型列表（截图见 ×0.06/×0.25 等当前列表不存在的倍率），
  需用户在 DSH 里删掉旧模型重新拉取，网关侧无法处理。
