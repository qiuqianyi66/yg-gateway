# fix-config-misdetect 变更摘要

> 2026-10-23 生产实弹：面板账号池全 0、healthz 503。双根因修复（配置层 + 代码层）。

需求：`docs/prd/REQ-2026-10-23-fix-config-misdetect.md`（L2 业务逻辑）
分级：L2
ADR：不写（小补丁，无难逆转 / 无真实权衡）

---

## 一、改了什么

| 文件 | 作用 / 改动 |
|---|---|
| `修改：cmd/server/main.go` | 新增 `configFileGenuinelyMissing(configPath, loadErr)` helper：先 `errors.Is(loadErr, fs.ErrNotExist)`，再 `os.Stat(configPath)` 确认 config 文件本体缺失才返回 true；main() 分支从 `if errors.Is(err, fs.ErrNotExist)` 改为 `if configFileGenuinelyMissing(*cfgPath, err)`。附属文件（prompt.file 等）缺失不再触发 auto-generate 兜底，改为 fail fast |
| `新增：cmd/server/main_test.go` | `TestConfigFileGenuinelyMissing` 4 case（见 PRD P1-4），`package main` 内部测试 |
| `修改：D:\yg-gateway\config.json`（生产，先备份） | `prompt.file: "./persona.md" → "D:/yg-gateway/persona.md"`；此前已做 `auth_dir`/`state_file` 绝对化 + 删重复 model_aliases 键（bak-20261023） |
| `新增：.harness/changes/fix-config-misdetect/` | PRD + 本 summary |

**没做的（明写，免得下轮重提）**：
- 工作区外部 WIP（cmd/credit/*、internal/server/handler.go、handler_fallback.go 等）未提交、未触碰；
- `internal/pool/state.go` gofmt 漂移（HEAD 既有）单独 style 提交，不在本提交；
- 未改动 go.mod / 工具链版本（toolchain 1.27.3 缓存损坏已通过重下载修复，环境操作不提交）。

## 二、怎么验证的

| 验证 | 命令 | 结果 |
|---|---|---|
| 定向单测 | `go test ./cmd/server -run TestConfigFileGenuinelyMissing -count=1` | ok（0.060s） |
| 全量测试 | `go test ./... -count=1` | 23 包全绿（20 ok + 3 no test files） |
| vet | `go vet ./...` | exit=0 |
| gofmt | toolchain 1.27.3 `gofmt -l ./cmd ./internal` | 仅剩 state.go（已单独修） |
| 边界 | `scripts/check-boundaries.ps1` | ✅ 通过（exit=0） |
| 体量棘轮 | `scripts/check-depth.ps1` | ✅ 未回退（maxFileLines 1089→1088） |
| 生产复现（修复前） | `C:\` 启动带重定向 | `config ... not found (auto-generate failed): prompt file ./persona.md ...` + `loaded 0 account(s)` + api_key=false + healthz 503 |
| 生产验证（修复后） | `C:\` 启动带重定向 | `loaded 4 account(s) from D:/yg-gateway/auths` + healthz `{"healthy":3,...,"total":4}` + api_key=true |

**负向探针**：修复后代码层——config 文件存在但 prompt.file 缺失 → 必须 fail fast 而非兜底，
由单测 case 1（want=false）锁定；修复前该场景实跑即 503（上表生产复现行）。

## 三、怎么回滚

`git revert <本提交>`；生产 config 备份 `D:\yg-gateway\config.json.bak-20261023`（恢复 prompt.file 相对路径即回旧行为，但不要——bug 依旧存在）。生产 exe 换版走 `scripts/deploy-prod.ps1`（备份→冒烟→失败自动回滚）。
