# 变更摘要：F-101 凭证 ACL 常态加固（2026-10-21）

> 关联：`docs/prd/REQ-yg-gateway-deep-optimization-2026-10-12.md` F-101（P0）
> 用户指令：「继续去做呗」——按交接件 §五 待决策项，同意落地 F-101。
> 分级：L2（业务逻辑 · 安全加固）

## 一、改了什么

把「新建凭证后手动跑 `lock-auths.ps1`」（R1 唯一人工兜底）改为**代码内自动收紧**：
Windows 上每次凭证写回后与启动加载时，自动执行与脚本同语义的 `icacls` 收紧
（`/inheritance:r /grant:r <me>:(F) /grant:r "NT AUTHORITY\SYSTEM:(F)"`），只收窄不赋权。

| 文件 | 改动类型 | 说明 |
|---|---|---|
| `internal/auth/lockdown.go` | 新增 | `LockdownFile`：平台/runner 双注入点，非 Windows 跳过，失败返回错误 |
| `internal/auth/auth.go` | 修改 | `SaveAtomic` 写回成功后 + `LoadDirReport` 启动兜底各调一次（失败 WARN 不阻断） |
| `internal/auth/lockdown_test.go` | 新增 | 5 用例：非 Windows 跳过 / Windows 参数形状 / 失败报错 / SaveAtomic 触发 / LoadDir 逐文件兜底 |
| `README.md` | 修改 | 安全基线第 1 条补「服务已内置自动收紧，脚本仅作手动兜底」 |
| `docs/adr/ADR-012-auth-acl-lockdown.md` | 新增 | 选型权衡：icacls（os/exec）vs x/sys ACL API |

**没做的（明写，免得下轮重提）**：
- 不新增 `golang.org/x/sys` 直接依赖（保持 go.mod 直接依赖个位数，理由见 ADR-012）。
- 不改 `lock-auths.ps1`（保留为手动兜底与一次性全量修复工具）。
- 不改 PRD 状态字段（`REQ-yg-gateway-deep-optimization-2026-10-12.md` 是整轮聚合 PRD，
  单条落地不翻整轮状态；实现记录在本文件 + ADR）。

## 二、怎么验证的

| 验证 | 命令 | 结果 |
|---|---|---|
| 包测试 | `go test ./internal/auth/ -count=1` | ok（含 5 个新用例） |
| 格式 | `gofmt -l internal\auth` | 无输出 |
| 全量门禁 | `powershell -NoProfile -File scripts/pre-commit.ps1` | 见下 |

**负向探针**（证明检查真的有牙齿）：临时注释 `SaveAtomic` 内的 `LockdownFile` 调用 →
`go test -run TestSaveAtomic_LockdownInvoked` 预期报红 → **实测 FAIL（got 0 calls）** →
探针已删除、挂点恢复、全量转绿。新测试均**不** `t.Parallel`（全局注入点与包内既有
并行测试共享，非 Parallel 先于 Parallel 串行执行，替换期间无并发触碰者）。

## 三、未完成 / 待补（诚实清单）

- [ ] 生产实例（`D:\yg-gateway`，Windows）实际落盘一次后复验 ACL：`(Get-Acl .\auths\*.json).AccessToString`
  应只剩服务账户 + `SYSTEM`，无 `BUILTIN\Users`（requires：下一次部署换版到生产后抽查；
  本机开发环境无 `auths/` 目录，测试用假 runner 无法覆盖真实 icacls 执行路径）。

## 四、怎么回滚

- `git revert` 本变更提交即可；代码回退后行为回到「人工跑脚本」基线。
- 已收紧的文件 ACL **不会**因回滚而恢复宽松（icacls 收紧不可逆地改写了继承 ACL，
  `/inheritance:r` 断开后需手动重建继承）——回滚只影响后续新写入的文件。

## 五、Learning

根因：Windows 上 `0o600` 与 `os.Rename` 都不改写继承 ACL，新建/替换的凭证文件默认对
`BUILTIN\Users` 可读——「原子写回」正确 ≠ 「文件权限正确」，两者是独立属性，加固
必须挂在写回路径上而非仅文档指引。
