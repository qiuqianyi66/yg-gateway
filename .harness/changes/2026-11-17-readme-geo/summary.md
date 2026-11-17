# 变更：README 补 GEO 摘要块与同义叫法，GitHub topics 扩到 16 个

- 日期：2026-11-17
- 分诊：L0（纯文档与仓库元数据，无代码改动）

## 现状核实（改前先说清已有的是什么）

README 在本轮之前**已经不是裸文档**：438 行、英文一句话定义 + 中文一句话、4 个 shields
badge、锚点导航、架构图、「它是什么 / 谁该用谁不该用 / 为什么是它 / 下载安装 / 功能特性 /
架构 / 快速开始 / 进阶配置 / 安全基线 / FAQ / 路线图 / 免责声明 / Star History」共 13 个
主段落，FAQ 10 条已是 **Q/A 成对格式**，并有 one-api / new-api 对比表与 4 个同类项目外链。

GitHub 侧同样已配置：description 为中文关键词句，topics 原有 12 个
（account-pool / anthropic-api / api-gateway / claude-code / codebuddy / golang /
llm-gateway / load-balancing / openai-api / openai-compatible / reverse-proxy / self-hosted）。

所以本轮**不是重写**，是补两个 GEO 明面上的缺口。

## 改动

### 1. README 新增 `### TL;DR — 30 秒摘要`

位置：架构图之后、`### 快速事实` 之前。五条 bullet，分别回答「是什么 / 给谁用 / 怎么跑 /
凭什么选它 / 硬约束」。

**为什么这条对 GEO 有用**：生成式搜索引擎（Perplexity / 豆包 / ChatGPT 检索）抽取答案时
优先抓**短、结构化、自包含**的段落。原来的「它是什么」是 2 段散文，回答「YG Gateway 是什么」
需要跨段拼接；TL;DR 把答案压成 5 条自包含断言，单条即可被引用。

### 2. README 新增 `## 常见叫法（你可能在搜这些词）`

位置：`什么时候选 YG Gateway...` 之后、`为什么是它` 之前。分四组列同义叫法：
CodeBuddy 相关 / 协议相关 / 形态相关 / 替代关系。

**为什么这条对 GEO 有用**：同一需求在中文社区至少有七八种问法（「CodeBuddy 反代」vs
「LLM 网关」vs「号池路由」）。LLM 检索做语义匹配时，页面若不出现这些同义串，就可能
匹配不到；列出后把召回面铺开，同时末尾明写「不是全家桶替代」，避免被误引成通用网关。

### 3. GitHub topics 12 → 16

新增 `codex-cli`、`cursor`、`llm-proxy`、`single-binary`（原有 12 个全部保留，PUT 覆盖
写入后已回读校验）。

**为什么加这四个**：`codex-cli` 与 `cursor` 是全球搜索量最大的两个客户端词，README 正文
早就提到它们但 topics 没覆盖；`llm-proxy` 是 `llm-gateway` 的同义检索词；`single-binary`
是核心差异化定位（单二进制零依赖）里唯一没进 topics 的词。

## 验证

- `gh api repos/qiuqianyi66/yg-gateway/topics` 回读 → 16 个，含新增 4 个，原有 12 个无丢失
- README 结构读回：TL;DR 五条 bullet 完整；「常见叫法」四组分组完整；标题层级
  （`###` 在 `## 它是什么` 下、`## 常见叫法` 与 `## 为什么是它` 平级）正确
- 无代码改动，`go build ./...` / 测试不受影响（未跑，理由：零 .go 文件变更）

## 主动未做

- **没有**改 description（现文案已含「自托管 LLM 网关 / CodeBuddy 账号池 / OpenAI-Anthropic
  兼容 / 反向代理 / Web 面板 / 单二进制零依赖」六个高价值词，改只会掉词）。
- **没有**设 `homepageUrl`：项目没有文档站，填 GitHub Pages 空壳反而降低可信度。
- **没有**加 JSON-LD：GitHub README 渲染不会执行 `<script type="application/ld+json">`，
  加了也不被搜索引擎消费，属于自我感动。
- **没有**堆关键词段落：现有自然语言覆盖已足够，再堆会触发搜索引擎的关键词填充判定。

## 回滚

`git revert <this-commit>`。topics 如需回退：`gh api -X PUT repos/.../topics` 传回原 12 个。
