# 多 API Key 派发（multi-api-key）变更摘要

> ⚠ **本文件是 2026-10-20 事后补记**，不是当时写的。触发原因：新增检项
> `scripts/check-harness.ps1` [6]（每个 `changes/<feat>/` 必须含 `summary.md`）
> 报到本目录缺失。内容依据本目录 `PLAN.md` / `tasks.md`、`docs/adr/ADR-001`、
> `docs/prd/REQ-yg-gateway-multi-api-key-2026-10-11.md` 与 git 历史重建，
> **未逐行复核代码**——凡未能从上述材料确证的一律标 unknown。

需求：`docs/prd/REQ-yg-gateway-multi-api-key-2026-10-11.md`
ADR：`docs/adr/ADR-001-multi-api-key.md`
分级：L3（架构 / 数据模型 / 鉴权链路承重墙）
复杂度：C 耦合（鉴权 + 存储 + 面板 + 派发，跨模块责任）

## 一、改了什么

把「单 api_key 自用」升级为「配置驱动的多 Key 派发」。

| 层 | 落点（据 PLAN.md） |
|---|---|
| 鉴权 | `internal/httpauth`：前缀索引 + 常量时间比较 |
| 配置 | `internal/livecfg` / `cmd/server`：多 Key 解析、三态 models、空值契约 |
| 脱敏 | `loadConfigMasked` 回显脱敏；`api_key_configured` 状态字段 |
| 面板 | `internal/panel`：多 Key 派发展示（管理 UI 属二期 panel-key-management） |
| 落库 | `internal/reqlog`：请求日志带 Key 标识 |

git 侧证据（`git log --all`）：`92ea9e6 feat(config): 多 API Key 配置解析 + 三态 models + 空值契约`、
`2501ce8 test(multi-api-key): T7 配置保存回归 + benchmem 基准`、
`9823ebd fix(config): 顶层 api_key 与多 Key 的重复检测同口径（B3-c）`。

**没做的**：未做充值 / 兑换码 / OIDC（项目定位红线，至今有效）。

## 二、怎么验证的（据 PLAN.md 转述，非本次实测）

- T0~T7 先测试后实现，`internal/httpauth` 有 `httpauth_test.go` / `index_test.go` / `bench_test.go`
- 三组代码审查通过（5 问题修复）；两轮最终审查通过（6 阻断项修复，PLAN.md §最终审查列明）
- **本次（2026-10-20）未复跑上述验证**——它们的历史结论，不等于当前事实。要复现需重跑
  `go test ./internal/httpauth ./internal/livecfg ./internal/server ./internal/reqlog`

## 三、未完成 / 待补

- [ ] 本次补记未复核代码，上面「改了什么」来自 PLAN.md 与 ADR-001 的转述
- [ ] PLAN.md frontmatter 写 `status: 实现完成…待合并`；**当前合并状态未核实**
      （git 历史里有相关提交，但「待合并」是否已消除需查分支与 main 的差）

## 四、怎么回滚

代码回滚：`git revert` 相关提交（多 Key 是配置驱动能力，非数据迁移）。
配置回滚：`config.json` 的 `api_keys` 置空即退化单 Key 模式。
凭证目录 `auths/` 不参与回滚。

## 五、Learning

根因（本次补记得到的方法层教训）：**changes 目录缺 summary.md 不会自己报错**——
本目录躺了 9 天没人发现，直到把它变成机器检项。这类「治理缺口的缺口」正是
检项 [6] 存在的理由（见 `docs/prd/REQ-harness-engineering-2026-10-19.md` §五 F1-6）。
