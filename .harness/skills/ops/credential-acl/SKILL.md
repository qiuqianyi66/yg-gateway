---
name: credential-acl
description: 凭证 ACL 加固——auths/*.json 落盘权限（Windows icacls + Go 0600），加账号/换机器后必读。R1/R7 红线。
---

# 凭证 ACL 加固（YG Gateway）

## 触发条件

- 新增账号后（OAuth 登录 / 导入）
- 换机器部署后
- 怀疑凭证文件权限过宽

## 背景（已踩过的坑）

Go 的 `os.WriteFile(..., 0o600)` **在 Windows 上不剥离继承的 ACL**——文件从父目录继承
`BUILTIN\Users` 的读权限，导致凭证对同机其他用户可读。Linux 上 0600 有效，Windows 上
必须额外用 `icacls` 改 ACL。

## 工作流

1. **落盘权限**：auth 包写凭证用 0o600 + 原子写（tmp + rename）
2. **Windows ACL**：跑 `lock-auths.ps1`（用 icacls `/inheritance:r /grant:r`，
   **不要用 Set-Acl**——需要 SeSecurityPrivilege，非管理员会 PrivilegeNotHeld）
3. **验证**：`icacls auths\*.json` 确认无 `BUILTIN\Users:(R)` 继承项

## 红线

- R1：凭证（accessToken/refreshToken）禁入日志、state.json、归档 JSONL
- R7：凭证读写走 auth 包的加锁方法（AccessTokenValue/RefreshTokenValue），禁直读字段

## 自检

- [ ] `lock-auths.ps1` 已跑（Windows）
- [ ] icacls 验证无继承读权限
- [ ] 凭证未泄漏进日志/归档

## 下一步

凭证安全 → 无后续阶段（运维动作）。
