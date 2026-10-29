# 2026-10-28-models-rate-suffix — /v1/models 倍率后缀改拼 id

## 改了什么（observation）

外部轮遗留改动（非本会话产生，收尾时补变更目录）：

- `internal/server/models_handler.go`
  - 新增 `effectiveRateRaw(mi)`：返回模型当前生效倍率原文（`PromoCredits` 优先，否则
    `Credits`），不做 normalize，保留 "0x" / "x0.29" 原文格式以喂给 `nameRateSuffix`。
  - `applyModelInfoFields` 的 `name` 不再拼积分倍率后缀，只放显示名 + 促销标签
    （`GLM-5.2` / `Hy4 preview 限时免费`）。
  - `modelList()` CN 动态分支与 global 分支的 `id` 拼生效倍率后缀（`cn:hy3 ×0.05`、
    `global:xxx ×0`）；global 分支仅当 `globalInfos` 有该模型富字段时才拼后缀。
- `internal/server/resolve_model.go`
  - 新增 `stripRateSuffix(bare)`：按第一个空格截断，还原裸模型名（上游模型 id 均为
    `[a-z0-9-]+` 不含空格，安全）；`resolveModel` 全部四个分支已接入。
  - 旧客户端发不带后缀的 id（`cn:hy3`）不受影响——找不到空格原样返回。
- `internal/server/handler_test.go`：三处断言同步（`cn:glm-5.2 ×0.05`、name 纯显示名、
  `cn:hy4-preview ×0`、`Hy4 preview 限时免费`）。

## 为什么（背景）

第三方客户端（如 Cherry Studio）的"获取模型"弹窗只渲染 `id` 字段，之前倍率拼在
`name` 上它们看不到。把展示倍率后缀拼到 `id` 上，弹窗列表直接显示倍率；
路由时由 `stripRateSuffix` 剥掉，还原成上游认识的裸模型名，wire 语义零变化。

## 怎么验证的

- `go test ./internal/server`（handler_test 三处断言同步后全绿）
- 依赖方向：`resolve_model.go` / `models_handler.go` 均在 `internal/server`，无新跨包边。
- 倍率剥除安全性：上游模型 id 不含空格（既有事实），首个空格截断无损。

## 怎么回滚

- 若客户端对 id 带后缀出现路由异常：`git revert` 本提交即可（三个 Go 文件 + 变更目录）；
  `stripRateSuffix` 对无空格输入原样返回，旧客户端路径天然兼容。
