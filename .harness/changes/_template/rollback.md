# {feat-name} 回滚方案

> 用法：复制到 `.harness/changes/{feat-name}/rollback.md`。**先写回滚再动手**是纪律——
> 写不出来说明这次改动的风险没想清楚。

## 触发条件（什么情况下必须回滚）

- 冒烟失败：`/healthz`、`/v1/models`、流式 / 非流式任一不发
- 部署退出码 2（健康检查失败，脚本已自动回滚）或 4（旧版没能起来，**生产可能不可用**）
- 线上观测期指标越基线

## 回滚步骤

1. 二进制换版：恢复 `yg-gateway.exe.v{旧版本}` 备份，走
   `powershell -NoProfile -File scripts/deploy-prod.ps1 -Exe <旧exe>`
   （**不要手工分步做**：备份 → 等端口释放 → 替换 → 启动 → 健康轮询 → 冒烟是一条命令）
2. 纯文档 / 脚本改动：`git checkout -- <文件>`；新增文件 `rm`
3. 数据 / 状态：`state.json` 是否受影响？（凭证目录 `auths/` **不参与回滚**，删了要重新 OAuth）

## 回滚后必须验证

- `/healthz` 200、`/v1/models` 返回、流式与非流式各一发
- `git log --oneline -1` 与磁盘一致

## 已知风险 / 回滚不了的部份

（如实写；写「无」之前先确认真的无）
