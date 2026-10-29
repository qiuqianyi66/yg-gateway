# name 字段透出生效倍率后缀

## 改了什么

`/v1/models` 条目的 `name` 字段从「裸显示名 + 促销标签」改为「显示名 + 生效倍率后缀 + 促销标签」，
与 `id` 字段上的后缀同口径（`nameRateSuffix(effectiveRateRaw)`，promo 价优先）。

- 改前：`id="cn:glm-5.2 ×0.05"`，`name="GLM-5.2"`
- 改后：`id="cn:glm-5.2 ×0.05"`，`name="GLM-5.2 ×0.05"`
- promo 场景：`name="Hy4 preview ×0 限时免费"`

## 为什么

DSH 预览版客户端的模型下拉渲染 `name` 字段（连 `cn:` 前缀都不带），而倍率之前只拼在 `id` 上
（Cherry Studio 类弹窗渲染 id 才能看到）→ DSH 用户在列表里完全看不到倍率。
现在两类客户端（渲染 id / 渲染 name）列表都直接显示生效价。

## 边界

- 上游未给倍率（unknown）→ `nameRateSuffix` 返回 ""，name 不加后缀，不编造。
- 路由不受影响：客户端发请求用 id，`stripRateSuffix` 按第一个空格截断还原裸名；
  即便有客户端拿 name 发请求，剥空格后与改前裸名一致。
- `credits` 字段仍透牌价原文，`description` 前缀 `[xN credit]` 不变。

## 验收（Given-When-Then）

- Given 上游返回 `glm-5.2` credits=`x0.05 credits`，When GET /v1/models，Then name=`GLM-5.2 ×0.05`
- Given 上游 promo `hy4-preview` 生效价 `0x` + 标签「限时免费」，Then name=`Hy4 preview ×0 限时免费`
- Given 上游某模型未给 credits，Then name 不带倍率后缀

## 验证

- `go test ./internal/server/` ok（含 TestModelsEndpoint / TestModelsPromoFields 断言联动更新）
- `go vet ./internal/server/` 无输出；`gofmt -l ./cmd ./internal` 无输出
- `scripts/check-boundaries.ps1` 通过

## 回滚

 revert 这两个文件的本次改动即可，无配置/数据迁移。
