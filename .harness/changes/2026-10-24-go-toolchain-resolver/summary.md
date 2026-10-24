# 2026-10-24 go-toolchain-resolver

## 背景与必要性

本机系统 Go（`C:\Program Files\Go`，go1.27.0）自 10-22 起损坏：`go` 任何子命令报
`go binary is trimmed and GOROOT is not set`。实测设 `GOROOT` 环境变量无效（二进制本体坏，
非环境问题）；Program Files 非管理员不可写，无法就地修复。后果：四个硬编码该路径（或裸调
PATH `go`）的本地脚本全部失效，且 `check-boundaries.ps1` 只在最后 `go list` 处报错、
`check-depth.ps1` 的 `Test-Path` 挡不住「在位但损坏」，报错位置与语义都不直观。
本地防线实际下线（CI 用 setup-go 不受影响）。10-22 起的提交信息（"gate 验证待工具链恢复"）
证实该状态已持续多轮。

## 改了什么

`pre-commit.ps1` / `check-depth.ps1` / `check-boundaries.ps1` / `deploy-prod.ps1` 四处
统一改为 `Resolve-GoExe` 解析：候选 = `$env:YG_GO_EXE`（用户级覆盖，setx 即可，无需管理员）
→ `C:\Program Files\Go\bin\go.exe` → PATH `go`；**每个候选实跑 `go version` 验证**
（Test-Path 挡不住损坏的二进制），首个可用者胜出。

- 前三个脚本：全部候选失败 → 打印修复指引后 `exit 2`（各自语义均为「脚本自身失败」，
  不与门禁红/棘轮回退混淆）。
- `deploy-prod.ps1`：解析**惰性**（只在 `-Build` 分支调用——非 -Build 部署不需要 go，
  不该被拦）；失败走 `$exitCode = 1` + `stage = rejected-no-go-toolchain`（构建在停服前，
  「未触碰生产」语义，不占用退出码 2-4 的既有含义）。
- 四份函数为有意复制（脚本保持 standalone），失败语义各自内联。

## 怎么验证的

- `check-boundaries.ps1` 设 `YG_GO_EXE` 指向模块缓存工具链（1.27.3）实跑通过。
- `check-context-budget.ps1` / `check-harness.ps1` 照常通过。
- `deploy-prod.rehearsal.ps1` 全量排练通过（见下）。
- 全量 `pre-commit.ps1` 通过。

## 怎么回滚

单提交 revert 即可；四脚本各自的 `$goExe` 使用点未变（只换了解析方式）。

## 遗留（非本变更范围）

- 系统 Go 二进制本体需管理员重装（≥ go.mod 的 1.27.3）；装机前以
  `setx YG_GO_EXE <工具链 go.exe>` 恢复本地门禁。
- 面板 `internal/panel/login.go:211` 的判据注释被 Mimosa 安全钩子拦截未落地（钩子把
  该文件既有 `epAcct+state` 判为 SSRF，属误报——state 在上方已过会话校验）。该 `_ =`
  为存量行，R10 门禁只查新增行，不受影响；待钩子规则校准后补。
