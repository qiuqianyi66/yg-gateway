# 回滚路线 — 2026-10-22-bugfix-panel-reqlog

纯代码 bugfix，无数据迁移、无配置变更、无二进制部署。

- 整包回滚：`git revert <commit>`。
- 部分回滚：`git checkout HEAD~1 -- internal/reqlog internal/panel`（测试文件随代码一起还原）。
- 本改动不写 `auths/`、不动 state.json、不动面板 embed 资源；回滚后无需恢复任何数据。
