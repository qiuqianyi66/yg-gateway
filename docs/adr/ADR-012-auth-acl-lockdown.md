# ADR-012：凭证 ACL 收紧的实现选型（icacls via os/exec）

- 日期：2026-10-21
- 关联：F-101（`docs/prd/REQ-yg-gateway-deep-optimization-2026-10-12.md`）、`lock-auths.ps1`
- 状态：已采纳

## 背景

Windows 上 Go 的 `0o600` 与 `os.Rename` 都不改写继承 ACL，新建/替换的 `auths/*.json`
默认对 `BUILTIN\Users` 可读（明文 token 可被本机任意登录用户冒用）。F-101 要求把
「每次加账号后手动跑 `lock-auths.ps1`」变成代码内自动收紧。实现方式存在真实权衡。

## 候选

1. **`os/exec` 调 `icacls`**（采纳）：与 `lock-auths.ps1` 完全同语义，一行参数照抄；
   icacls 是 Windows 自带、已验证（脚本已生产使用）；失败时 stderr 信息可读（SID 反查名、
   拒绝原因等）；`exec.Command` 不经 shell，参数数组无引号转义问题。
2. **`golang.org/x/sys/windows` ACL API**：不 spawn 进程；但需手写
   `SECURITY_DESCRIPTOR` / `EXPLICIT_ACCESS` / `SetEntriesInAcl` / `SetNamedSecurityInfo`
   全套（继承标志、SID 构造易错），且把 `x/sys` 从 indirect 升为直接依赖（go.mod
   直接依赖数 +1）。

## 决策

采纳方案 1。理由：

- **语义零漂移**：脚本与代码共用同一 icacls 命令形态，「人工兜底」与「自动收紧」结果一致，
  审计时对同一命令的两处引用不会打架。
- **可读性/可测性**：runner 注入点使 Linux CI 可测全部分支；ACL API 方案的错误面
  （结构体字段拼错、SID 序列化错）在 CI 上无法用假实现验证。
- **代价可控**：spawn 进程仅在低频写回路径（登录/导入/token 刷新，小时级）与启动加载，
  不在热路径；依赖 icacls 存在于系统——Windows 自带，非 Windows 分支直接跳过。

## 代价与边界

- 每次写回额外一次外部进程调用（低频，可接受；幂等）。
- 收紧只影响 Windows；Linux/容器部署跳过（威胁模型不同：挂载目录/umask 负责）。
- 失败不阻断写回/启动（WARN 记录，R10），acl 加固是次级防御。
