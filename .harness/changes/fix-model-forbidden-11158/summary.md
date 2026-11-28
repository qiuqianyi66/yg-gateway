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

- `go test ./internal/server -run TestModelForbiddenDoesNotPenalizeOr503 -count=20 -shuffle=on`
  PASS（2026-11-28 复跑）。
- 全量 `go test ./... -count=2 -shuffle=on` 全绿；`scripts/pre-commit.ps1` 11/11。
- 生产证据（部署前）：`D:\yg-gateway\data\logs\yg-gateway-20261126-195857.err.log`
  21:55:12 两条 `11158`（免费账号请求 `hy3`），旧行为退化成 503。
- 生产验证（部署后，2026-11-28 20:27）：
  - `scripts/deploy-prod.ps1 -Build -SkipSmoke -Feature model-forbidden-11158`
    退出 0，健康 `healthy=2/4`，报告
    `D:\yg-gateway\data\logs\deploy-prod-20261128-202744.json`。
  - 免费模型 `cn:hy3` 探针返回 **400 `model_forbidden`**，message 含
    `11158 ... requestId ...`；stderr 记录 `upstream 400 model_forbidden`，不再是
    `client`/`no_healthy_account`。
  - 探针后 `/status` 两个账号 `consecutive_fails=0`、`breaker_fails=0`，未罚号。
  - `yg-regression-test.py --model cn:hy4flash-free --skip S4,S8 -n 10`
    **14 PASS / 0 FAIL**（免费额度，credit=0，无付费回退）。

## 怎么回滚

- 还原上述三个生产文件并删除测试/本记录；无配置或数据迁移。
- 已归因的旧 503 行不动（审计事实）；新部署后同类请求应变为 400。
