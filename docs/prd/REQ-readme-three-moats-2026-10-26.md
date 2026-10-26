# REQ-readme-three-moats-2026-10-26 — README 三护城河 + Magpie/CC Switch 章节（ROADMAP v3 V3-13）

## 必要性（阶段 0）

1. **真实问题**：README 的 2×2 格子讲的是"能跑起来"的门槛（单二进制/养号/安全/治理），
   v3 深化期真正打穿的三件事——实测成本分层、健康治理、决策溯源——只散落在
   `docs/reference/` 里，新访客看 README 会把本项目当成"又一个套壳 CodeBuddy 网关"。
2. **不做会怎样**：三护城河的差异化叙事在开源侧不可见；magpie 抖音推广带起来的
   赛道拉力接不住，过来的人只看到"又一个 workbuddy2api"。
3. **更便宜手段**：在现有 README 里加两节（~40 行），不动代码、不动面板。
4. **依据**：ROADMAP v3 V3-13；magpie 抖音两支推广视频核验（2026-10-18，
   `docs/reference/magpie-market-*`）——市场教育由它完成，我们接深耕人群。

**结论：做。**

## 需求

1. README「为什么选 YG Gateway」2×2 之后加「三护城河」小节：三行表格，每行
   一个护城河（实测成本分层/健康治理/决策溯源），列：做法、同类现状。
   同类现状的措辞**必须有 magpie 八维对比的实证依据**（`docs/reference/magpie-deep-read-2026-10-25.md` §二），
   不许写"全网首创/唯一"这类无法证伪的词。
2. README「接入客户端」之后加「与 Magpie / CC Switch 配合」一节：一句话定位
   （正交、可叠加）+ 两段 base_url/api_key 配置示例。不写"替代 magpie"——
   它们管 Claude Code provider 切换，我们管 CodeBuddy 池，互补不互斥。
3. 不动其他章节；不动 TOC（新增节没有锚点链接引用）。

## Given-When-Then 验收

- **G1 三护城河可见**：When 访客打开 README，Then 在「为什么选」段落下方能看到
  三行表格，每行对应一个护城河，且「同类现状」列措辞克制（不出现"唯一/首家/全网"）。
- **G2 Magpie/CC Switch 配置可复制**：When 用户按 README 贴出 base_url 和 api_key
  到 Magpie/CC Switch，Then 无需再查文档就能接上（两行配置齐）。
- **G3 事实可追溯**：When 评审人质疑"同类空白"措辞，Then 能从
  `docs/reference/magpie-deep-read-2026-10-25.md` §二找到对应行（成本分层/健康/决策
  三行逐一对得上）。
- **G4 零回归**：When markdown 渲染（GitHub），Then 表格不串列、链接不断、
  原有章节顺序不变。

## 落点

- `README.md`：新增两节。
- 变更目录：`.harness/changes/2026-10-26-readme-three-moats/`。
- 不改代码、不改配置、不改测试。
