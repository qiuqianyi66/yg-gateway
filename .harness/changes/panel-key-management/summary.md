# 面板 Key 管理页（panel-key-management）变更摘要

> ⚠ **本文件是 2026-10-20 事后补记**，不是当时写的。触发原因：新增检项
> `scripts/check-harness.ps1` [6]（每个 `changes/<feat>/` 必须含 `summary.md`）
> 报到本目录缺失。内容依据本目录 `PLAN.md`、`docs/adr/ADR-002`、
> `docs/prd/REQ-yg-gateway-panel-key-management-2026-10-11.md` 与 git 历史重建，
> **未逐行复核代码**——凡未能从上述材料确证的一律标 unknown。

需求：`docs/prd/REQ-yg-gateway-panel-key-management-2026-10-11.md`
ADR：`docs/adr/ADR-002-panel-key-management.md`（承重墙声明：**零后端改动**，仅前端）
分级：L1（面板前端）

## 一、改了什么

给一期（ADR-001）的多 Key 鉴权能力套上面板 UI：列表 / 新增 / 编辑 / 删除 / 启停 / 一键生成。

| 文件 | 改动（据 PLAN.md T0-T3） |
|---|---|
| `internal/panel/index.html` | 导航加「API Key」项；新增 `view-keys` section（表头 + 卡片容器 + 空状态 + 新增/刷新按钮） |
| `internal/panel/app.js` | `go()` 路由加 `keys`；`loadKeys()` / `renderKeyList()` / `keyCardHTML()` / `openKeyDialog()` / `deleteKey()` / `saveKeyFromDialog()` / `genApiKey()` / `showKeyPlaintext()` |

ADR-002 的关键决策（D-301 独立视图不嵌入配置页；D-303 卡片+弹窗而非行内编辑；
D-304 删除最后一条提交 null 退化为单 Key 模式）。

git 侧证据：`c5ce68d feat(panel): API Key 管理页（二期…）`、`33bdfe9 fix(panel): API Key 管理页改为卡片+弹窗风格`。

**没做的**：不动后端（ADR-002 承重墙）；不改 `view-config` 既有内容。

## 二、怎么验证的（据 PLAN.md T4 转述，非本次实测）

- `node --check app.js` 语法通过
- HTTP 冒烟：`view-keys` / `key-card` / `key-dlg` / `keyEditVeil` / `keyShowVeil` 全部 OK
- CSP 兼容：无行内 onclick
- PLAN.md 记「浏览器人工验证：待 YG 在 http://127.0.0.1:7863/panel/#keys 实际点一遍」——
  **该人工验证是否已完成，材料里没有结论 → unknown**

本次（2026-10-20）实测验尸：`grep -c view-keys internal/panel/app.js` = 1，代码仍在。
未重跑冒烟。

## 三、未完成 / 待补

- [ ] 浏览器人工验证是否做过：unknown（PLAN.md 记为「待 YG」）
- [ ] 本次补记未复核前端逻辑

## 四、怎么回滚

纯前端（`index.html` + `app.js`），且面板 JS 是 `go:embed` 进二进制的：
回滚要**重新编译二进制**再走 `deploy-prod.ps1`，不是 `git checkout` 就完事。
后端零改动，无数据迁移，无凭证影响。

## 五、Learning

根因（本次补记得到的方法层教训）：同上——**changes 目录缺 summary.md 不会自己报错**。
另：PLAN.md 里「待人工验证」这类收尾项如果不落成 summary 的待补清单，就会
永远停在「待」，因为它不在任何机器检查的覆盖面上。
