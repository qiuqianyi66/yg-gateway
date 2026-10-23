# REQ-2026-10-23-fix-config-misdetect：config 加载误判修复（prompt.file 相对路径实弹）

## 必要性（阶段 0）

1. **真实问题**：2026-10-23 生产实弹——面板账号池全 0、healthz 503 `{"healthy":0}`。
   双根因：
   - 配置层：`D:\yg-gateway\config.json` 的 `prompt.file="./persona.md"` 是相对路径，
     从错误工作目录（如 `C:\`）启动时读取失败返回 `fs.ErrNotExist`；
   - 代码层：`cmd/server/main.go` 只按 `errors.Is(err, fs.ErrNotExist)` 判断，把
     prompt.file 等**附属文件**的缺失误判成「config 文件不存在」，走进 auto-generate
     分支后兜底 `Load("")` —— 整份正常配置被丢弃，账号池清空、api_key 丢失。
2. **不做会怎样**：任何工作目录不匹配的启动方式（桌面双击、计划任务、第三方拉起、
   运维脚本 cwd 不同）都会静默退化成 0 账号且不报错——比功能缺失更危险（不报错）。
3. **现状是否已变**：生产 config 已修复（prompt.file 绝对化），错误 cwd 启动实测
   `loaded 4 account(s)`；代码层误判 bug 仍存在（本文档修复目标）。
4. **更便宜手段**：判定条件收窄一行 helper + 单测，无更便宜替代。

**结论：做**（L2，只动 cmd/server/main.go + main_test.go + 生产 config 同步）。

## 范围

| 编号 | 文件 | 验收（Given-When-Then） |
|---|---|---|
| P1-1 | cmd/server/main.go | Given config 文件存在但附属文件（prompt.file）缺失，Load 返回 ErrNotExist；When main 判定；Then 不走 auto-generate，直接 `load config: ...` 报错退出（fail fast），不静默兜底默认配置 |
| P1-2 | cmd/server/main.go | Given config 文件本身不存在；When main 判定；Then 走 auto-generate（首次运行行为不变） |
| P1-3 | cmd/server/main.go | Given 加载错误非 ErrNotExist（如 JSON 语法错）；When main 判定；Then 不兜底，报错退出（行为不变） |
| P1-4 | cmd/server/main_test.go | 新增 `TestConfigFileGenuinelyMissing` 4 case（文件存在+ErrNotExist→false；文件缺失→true；非 ErrNotExist→false；路径是目录→false），`go test ./cmd/server -run TestConfigFileGenuinelyMissing` 通过 |
| P1-5 | D:\yg-gateway\config.json（生产） | prompt.file 改为绝对路径 `D:/yg-gateway/persona.md`；从 `C:\` 启动仍加载 4 账号、healthz 非 503 |

## 不做（显式排除）

- 不碰工作区外部 WIP（cmd/credit/*、internal/server/handler.go、handler_fallback.go 等）；
  提交只含本任务文件（L-21）。
- `internal/pool/state.go` 的 gofmt 漂移是 HEAD 既有问题（764303a 未跑全量 gate），
  单独 style 提交修复，不混入本 bugfix 提交。

## 回滚

`git revert <本提交>`；生产 config 已留备份 `D:\yg-gateway\config.json.bak-20261023`；
无数据迁移、无上游协议变更，回滚零副作用。
