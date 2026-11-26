# fix-model-forbidden-11158

## 改了什么

- `internal/upstream/errors.go`：新增 `ErrModelForbidden`，识别 400 + 业务码
  `11158` / `not allowed for user type`，证据等级 High/Medium。
- `internal/server/handler_errpolicy.go`：ErrModelForbidden 零惩罚（不冷却、
  不 NoteError/NoteFailures），允许轮转。
- `internal/server/chatflow.go`：末端映射 400 + `model_forbidden`，上游原文透传。
- 新增 `internal/server/model_forbidden_test.go`：两个免费账号都 11158 时
  400 原文、无 no_healthy_account、账户 fails/err_total 不增加。

## 怎么验证

- `go test ./internal/server -run TestModelForbiddenDoesNotPenalizeOr503 -count=1`
  PASS。
- `go test ./internal/server -count=20`（后续批量复跑）与
  `scripts/pre-commit.ps1` 结果见本轮报告。
- 生产证据：`D:\yg-gateway\data\logs\yg-gateway-20261126-195857.err.log`
  21:55:12 两条 `11158`（免费账号请求 `hy3`）。

## 怎么回滚

- 还原上述三个生产文件并删除测试/本记录；无配置或数据迁移。
- 已归因的旧 503 行不动（审计事实）；新部署后同类请求应变为 400。
