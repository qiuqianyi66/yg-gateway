---
name: deploy-verify
version: 1
description: 部署验证——二进制 swap + 备份（R15）+ healthz 冒烟 + 观测期。【跳过】纯本地实验不替换任何运行实例；只改配置不换二进制（但仍要验证配置生效）。冒烟在任何替换二进制的场景都不可省略。
---

# Deploy Verify（YG Gateway）

## 触发条件

- 阶段 8-10：部署二进制、冒烟、观测。

## 工作流

1. **构建**：`CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o yg-gateway.exe ./cmd/server`
2. **备份**（R15，不可省略）：`Copy-Item yg-gateway.exe bin\yg-gateway.exe.v{版本}`
3. **停旧**：`Get-NetTCPConnection -LocalPort 7863 -State Listen | Stop-Process`
4. **swap + 启动**：构建/swap/启动**放进同一条命令**（跨命令间新 exe 可能被实时防护删除）
5. **冒烟**（不可省略）：
   - `GET /healthz` → 200，`service=yg-gateway`，healthy 计数正确
   - `GET /v1/models` → 200，模型列表非空
   - `POST /v1/chat/completions` → 流式正常（改动涉及 chat 链路时）
   - 面板 `/panel/` → 安全头齐全（CSP / nosniff / DENY / no-referrer）
6. **观测**：启动日志无 config 报错；错误率/上游调用在基线内。

## 回滚路线

- 冒烟失败 → 恢复 `bin\yg-gateway.exe.v{旧版本}` + 重启
- `auths/` 不随二进制回滚（删除即不可恢复）
- 状态落 `data/state.json`，回滚二进制不动状态

## 环境陷阱（已踩过）

- **实时防护删新 exe**：构建出的 exe 跨命令间可能被 Defender 删除。对策：构建+swap+启动放同一条命令，构建后立即 `Test-Path` 确认。
- **PowerShell 引号**：`\"` 不是转义（反斜杠不转义），commit 消息含引号用 `-F` 文件传。
- **Go 工具链**：本机 `$env:TEMP\go\bin\go.exe`（Go 1.27.2），GOPROXY 用 goproxy.cn。

## 自检

- [ ] 备份就位（R15）
- [ ] healthz / models / chat 冒烟通过
- [ ] 安全头齐全（面板）
- [ ] 启动日志无 config 报错

## 下一步

部署完成 → 观测期通过 → 收尾更新 summary.md。
