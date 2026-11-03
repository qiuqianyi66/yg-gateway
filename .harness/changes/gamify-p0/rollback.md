# gamify-p0 回滚方案

> 先写回滚再动手是纪律。本改动未部署生产，回滚 = git 还原。

## 触发条件（什么情况下必须回滚）

- 面板页面 404 / 白屏：新增资源路由或 HTML 引用出错（浏览器控制台 404 `/panel/gamify.css`、
  `/panel/app_views_gamify.js`）。
- 动效异常：toast 图标错位、彩屑遮挡操作、连续天数条破坏任务中心布局。
- 评审发现违反红线：零依赖、四态纪律（连续天数冒充上游）、reduced-motion 不可用。

## 回滚步骤

1. 还原既有文件（无二进制换版，未部署生产）：
   `git checkout -- internal/panel/index.html internal/panel/app.js internal/panel/app_views_accounts.js internal/panel/index.go internal/panel/panel.go internal/panel/security_test.go`
2. 删除新增文件：
   `git rm internal/panel/app_views_gamify.js internal/panel/gamify.css internal/panel/gamify_test.go .harness/changes/gamify-p0/ docs/prd/REQ-yg-gateway-gamify-p0-2026-11-03.md`
3. 本地存储：浏览器 `yg_streak_v1` 键不删（回滚后无人读写，无害）。

## 回滚后必须验证

- `go test ./internal/panel/` 全绿；`scripts/pre-commit.ps1` 退出码 0（深度棘轮回到 1842 基线）。
- 若曾部署：`/healthz`、`/v1/models`、流式/非流式各一发（本改动未部署，此条仅备忘）。

## 已知风险 / 回滚不了的部份

- 浏览器 localStorage `yg_streak_v1` 是持久状态：回滚后残留数据不参与任何展示（代码已删），
  无数据风险；但若未来重新启用连续天数，旧记录会直接续用（日期键格式 `YYYY-MM-DD` 未变）。
- 无其他不可回滚项（纯静态资源 + 前端逻辑，go:embed 编译进二进制前都在工作区）。
