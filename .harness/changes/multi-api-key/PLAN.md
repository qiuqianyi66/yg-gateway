---
last_updated: 2026-10-11
status: 实现完成，最终审查已过（4 阻断项已修复），待合并
owner: YG
modification_level: L3（架构 / 数据模型 / 鉴权链路承重墙）
complexity: C 耦合（鉴权 + 存储 + 面板 + 派发，跨模块责任）
prd: docs/prd/REQ-yg-gateway-multi-api-key-2026-10-11.md
research: docs/reference/multi-key-competitive-notes.md
adr: docs/adr/ADR-001-multi-api-key.md
---

# 多 API Key 派发 — 执行计划（PLAN 级）

> 路线图第 1 条。定位：把「单 api_key 自用」升级为「多 Key 派发」，
> 是 YG Gateway 从"自用"到"给朋友/小团队用"的分水岭。
> 阶段 0 竞品研究已完成，阶段 1 PRD 已产出，等你拍板 D-200~D-205 后进 ADR 与编码。

---

## 〇、为什么先立计划而不是先写代码

本项目有承重墙：**选号/冷却/熔断、凭证链路、出站改写管线、上游协议报文**。
多 Key 派发同时触及「凭证链路」（鉴权口径要改）和「数据模型」（Key 存储要定），
属于 L3 全流程 + ADR 先行。L3 直接写代码 = 屎山。所以先走：

```
阶段 0（竞品研究）→ 拍板 3 件事 → PRD（request-analysis）→ ADR → 十阶段流水线
```

---

## 一、阶段 0：竞品密钥派发实现深挖（当前缺口，先补）

研究目标（每项都要落到**具体实现**，不是功能介绍）：

| # | 竞品 | 要挖透的点 |
|---|---|---|
| 1 | workbuddy-manager（ithtelab） | Key 存储结构（SQLite 表）、Key 哈希方式（明文/SHA256/bcrypt）、白名单匹配顺序（IP→模型→Key）、分组/限额模型 |
| 2 | workbuddy2api-hub（ardeyouxipianyi） | Key 存储（文件/SQLite）、增量用量聚合与 Key 关联、剩余用量优先调度的数据结构 |
| 3 | ARP（KaiXuanSama） | 双协议下的 Key 模型、扫码登录与 Key 派发的关系、GPLv3 对借鉴的约束 |

产出：`docs/reference/multi-key-competitive-notes.md`（研究笔记，落盘可追溯）。

**红线提醒**：竞品里 ARP 是 GPLv3，只借鉴**思路/数据结构设计**，不抄代码（GPL 传染）。

---

## 二、待你拍板的 3 件事（决定 PRD 方向）

| D-编号 | 决策 | 选项 | 建议 | 状态 |
|---|---|---|---|---|
| D-100 | 先做哪条路线图 | A 多 API Key 派发 / B OpenRouter 费率 / C 增量用量聚合 / D 客户端生态 | **A**：星标增长最直接，且是 B/C 的地基 | 待拍板 |
| D-101 | Key 存储方向 | A 文件（JSON，保单二进制哲学）/ B SQLite（对齐竞品，功能强） | 待阶段 0 研究后给结论 | 待拍板 |
| D-102 | 面板 Key 管理页 | A 做（可视化增删改）/ B 先不做（仅配置文件写多 Key） | **A 但要分期**：先做配置驱动的多 Key 鉴权，Key 管理页作为二期 | 待拍板 |

---

## 三、承重墙影响声明（L3 必写）

多 Key 派发**触及凭证链路承重墙**（httpauth 鉴权口径），必须显式声明：

1. **httpauth.VerifyBearer 的口径要扩展**：现在 `api_key` 单值 → 多 Key 匹配。
   这是承重墙内的改动，需要 ADR 记录「单 Key → 多 Key」的迁移与兼容策略。
2. **R5 红线（密钥常量时间比较）**：多 Key 匹配必须仍走常量时间或至少不比单 Key 更易时序侧信道。
3. **R1 红线（凭证不出 auths/）**：Key 存储无论文件还是 SQLite，都不能把 Key 明文写进日志/state.json/归档。
4. **R11（配置热生效标注）**：多 Key 若走配置文件，要明确「新增 Key 是否热生效」还是「需重启」。

---

## 四、执行顺序（阶段 0 完成后）

1. 阶段 0 竞品研究 → 研究笔记落盘
2. 你拍板 D-100/D-101/D-102
3. request-analysis 产出 PRD（`docs/prd/REQ-...multi-api-key...md`）
4. ADR 先行（Key 存储选型 + 鉴权口径迁移）
5. 十阶段流水线（需求→方案→拆分→编码→单测→评审→集成→预发→部署→观测）

---

## 五、本计划的边界（现在不做的事）

- ❌ 现在不写任何业务代码（阶段 0 没做完、3 件事没拍板）
- ❌ 现在不产出 PRD（PRD 依赖竞品研究结论）
- ✅ 现在只做：阶段 0 研究 + 把本计划立起来
