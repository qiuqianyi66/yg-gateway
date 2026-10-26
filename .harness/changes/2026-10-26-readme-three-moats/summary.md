# 2026-10-26 readme-three-moats（V3-13：README 三护城河 + Magpie/CC Switch 章节）

## 背景与必要性

ROADMAP v3 P2 项（V3-13）。README 2×2 格子讲的是"能跑起来"的门槛，v3 真正
打穿的三件事（实测成本分层/健康治理/决策溯源）只在 `docs/reference/` 里，开源
访客看不到。magpie 抖音推广带起赛道拉力，我们接深耕人群——叙事必须前置。

## 改了什么

`README.md`：

1. 「为什么选 YG Gateway」2×2 表格之后加「三护城河」小节——三行表格，每行
   一个护城河 + 做法 + 同类现状。措辞克制（"同类项目普遍没做透"/"基本是空白"），
   不用"唯一/首创/全网"等无法证伪的词；同类现状列的每一行都能从
   `docs/reference/magpie-deep-read-2026-10-25.md` §二八维对比里找到出处。
2. 「接入客户端」之后加「与 Magpie / CC Switch 配合」小节——一句话正交定位
   （它们管 Claude Code provider 切换，我们管 CodeBuddy 池）+ 两段 base_url /
   api_key 配置示例。

## 怎么验证的（GWT 兑现）

- **G1/G4**：Markdown 人工过一遍——表格三列对齐，新增节插在正确位置，原有
  章节顺序未动；GitHub 渲染预期正常（标准 GFM table，无特殊语法）。
- **G2**：Magpie 段给 `base_url = http://127.0.0.1:7863/v1`，CC Switch 段给
  `ANTHROPIC_BASE_URL = http://127.0.0.1:7863` + `ANTHROPIC_AUTH_TOKEN = 面板 Key`，
  与既有「接入客户端」表格的 base_url 口径逐位一致。
- **G3 事实可追溯**：
  - "实测成本分层" → magpie-deep-read §二第 32 行（magpie ❌ 只有目录价）。
  - "健康治理" → §二第 33 行（magpie ❌ 无成功率/延迟，手动 Try now）。
  - "决策溯源" → §二第 38 行（pickReason + 四态 + unknown，magpie ❌）。
- 门禁：pre-commit 11/11（纯文档改动，gofmt/边界/深度不受影响）。

## 怎么回滚

单提交 revert。纯文档，无代码/配置/行为影响。

## 诚实记录

- **"同类现状"是相对 magpie 的八维对比结论**，不是对所有同类项目的穷尽断言。
  README 里写"基本是空白或只做了一半"而非"全网唯一"——CRS / one-api / LiteLLM
  没逐个源码核对，只在 magpie 这份八维对比里钉死了证据。
- **Magpie/CC Switch 的具体 UI 字段名没写死**（它们版本演进会改），只写 base_url
  和 token 两个必填项；用户按自家版本对号入座。
- **独立复审**：本变更派子 agent 客观审查（见 summary 末尾 review 段）；子 agent
  配额不稳时按 PLAN-next §3 协议 2 败即弃，转主线自查。
