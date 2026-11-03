# gamify-p1 回滚方案

- 关联 PRD：`docs/prd/REQ-yg-gateway-gamify-p1-2026-11-03.md`
- 性质：纯前端 + 静态资源管线（L1），未部署生产；回滚 = git 撤销，无需换二进制

## 一、回滚步骤

1. 撤已提交改动（若已提交）：

```
git revert <P1提交号>   # 或
git checkout -- internal/panel/app_views_gamify.js internal/panel/gamify.css internal/panel/index.go internal/panel/panel.go internal/panel/app.js internal/panel/security_test.go internal/panel/trends.js internal/panel/frontend_bundle_test.go
git rm internal/panel/app_views_achievements.js docs/prd/REQ-yg-gateway-gamify-p1-2026-11-03.md
```

2. 验证回滚后：

```
go test ./internal/panel/          # 全绿
powershell -NoProfile -File scripts/pre-commit.ps1   # 退出码 0
```

## 二、注意事项

- **P0 遗留修复一并回退**：`renderStreak` 容器选择器从 `'view-taskscenter'` 回到 `'taskscenter'`
  会让连续天数条回到「静默不渲染」状态——即 P0 的原始行为。回滚后该条不再显示属预期；
  若生产面板此前从未显示过连续天数条（P0 未部署该改动前行为），回滚无感知差异。
- 本地存储键 `yg_streak_v1` / `yg_ach_v1` 不回滚、无害（派生展示，非权威源，自动重建）。
- 临时验收实例：`artifacts/manual-tests/gamify-p1-20261103/` 已 gitignore，删目录即清。
  防泄漏：临时 `config.json` 含随机测试 api_key，验收后删除（P0 先例）。

## 三、影响面

| 项 | 影响 |
|---|---|
| 生产 `D:\yg-gateway`（端口 7863） | 无（改动未上线） |
| 面板用户可见行为 | 成就带消失、趋势曲线回直线/无 3 日均值线、连续天数条消失（P0 原状） |
| 数据 | 无（纯前端展示；localStorage 键残留无害） |
