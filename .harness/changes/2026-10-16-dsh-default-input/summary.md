# 变更总结 — T9：dsh `cordis.patch.yml` 的图片能力（provider 级 `defaultInput`）

> 承接 `docs/superpowers/plans/HANDOFF-2026-10-16-depth-next.md` §3 的 T9 与 §7 的 D4/D5。
> **改的是本机 dsh 客户端配置**（`<local>\.dsh\profiles\desktop\cordis.patch.yml`），
> 不属于本仓源码；本目录保存改动记录 + 时点快照，用于「旧备份误还原」的取证。

## 一、改了什么（3 处，全部在 `llm-pi-ai` → `providers.yg-gateway` 下）

| # | 改动 | 依据 |
|---|---|---|
| 1 | 新增**provider 级** `defaultInput: ["text", "image"]` | D4 拍板：provider 级 + 保留 20 条逐条声明（条目级优先，互不冲突）；这条同时覆盖 `models:` 未列出**及将来新增**的条目 |
| 2 | 修掉 `displayName: YG Gateway`` 的**尾随反引号** | 上一会话用 PS 双引号串改写 YAML 时漏下的转义残骸（正是 `.harness/LESSONS.md` L-1 的指纹）；它会显示在模型选择器里 |
| 3 | 保留既有 20 条逐条 `input: ["text", "image"]` | D4：两者并存；条目级是已实测/已核对的那一层 |

未做的：**没有**删 `cn:auto` 的图片声明（D5 的实测支持它，见 §三）；**没有**删任何 `.bak-*` 备份（属用户文件，只登记不清理）。

## 二、根因链（已独立核对源码，不是转述）

`<local>\.dsh\profiles\node_modules\@deepseek-ai\dsh-llm-pi-ai\lib\index.js`：

| 行 | 事实 |
|---|---|
| `:906` | `const DEFAULT_INPUT = ["text"];` —— 桥接层默认值把「未知」写死成「不支持文本以外」 |
| `:993` | `defaultInput: z.array(z.union(MODALITIES)).default([...DEFAULT_INPUT])` —— **provider 级**配置项（与 `defaultContextWindow`/`defaultMaxTokens` 同级） |
| `:682` | `input: declaredInput(entry.input) ?? base?.input ?? [...request.defaultInput]` —— 解析优先级：**条目级 > 目录 base > provider 默认** |
| `:1069` | `const defaultInput = [...source.defaultInput ?? DEFAULT_INPUT]`，`:1070` 空数组直接抛错 |
| `:900-905` | 插件作者自己的取舍说明：**欠声明**（提前拒绝并点名模型）与**过声明**（图片已附着、消息已持久化后才被上游拒绝，会话会反复重试一个不可能成功的请求）**代价不等**，默认选欠声明 |

## 三、怎么验证（三类证据，实证优先）

### 1. 网关目录实测（观测，只读，零 credit）

`GET http://127.0.0.1:7863/v1/models`（本地生产实例，4 账号 / 3 healthy）：

- **21 个模型全部 `supports_images: true`**；
- 与 patch 里的 20 条声明逐条比对：**过声明 = 0**、**欠声明 = 1**（`cn:mimo-v2.6-pro` 在目录里但 patch 未列出——
  正是 provider 级 `defaultInput` 要覆盖的那一类）。

### 2. 实发图核验（行为级证据，2 次调用 / 共 0.06 credit）

向 `cn:auto` 发一张 64×64 纯红 PNG（data URL 形态）：

| 次 | 落地模型 | 结果 |
|---|---|---|
| 1 | `kimi-k2.7`（`cn:auto` 的路由结果） | 200；回复抱怨「内容显示为乱码」（32 token 截断） |
| 2 | `glm-5.3-flash` | 200；回复**「图片只有一块纯红色，无有效信息」** ← **模型确实看到了图片** |

结论：图片链路（网关入站 → 上游报文 → 模型）**通**；`cn:auto` 声明图片与实测一致。

### 3. 结构校验与差异核对

- YAML 结构：用 js-yaml 把 4 处 `!!js` 表达式占位后解析通过——顶层 14 条目，
  `displayName = "YG Gateway"`、`defaultInput = ["text","image"]`、`models = 20` 且条目 `input` 全为数组。
- 与备份逐行 diff：只有 §一 的 3 处（1 处新增行组 + 1 处修正），无其它改动。

### 4. **更正上一份交接文档的一处验证方案（重要）**

HANDOFF §3 T9 写：「用网关 reqlog 归档 JSONL 确认 `image_url` 真出站」。
**该方案不可行**：reqlog 记录字段是
`time / request_id / path / account / model / status / ok / outcome / duration_ms / ttfb_ms / attempts /
prompt_tokens / completion_tokens / total_tokens / credit / credit_known / cache_hit_tokens /
cache_miss_tokens / client_ip / user_agent / key_id` —— **不含请求体**，
所以无法从中看到 `image_url`。本次改用**行为级证据**（模型能描述图片内容），比 grep 日志更强：
它证明图片不只是「发出去了」，而是被模型**理解**了。

## 四、怎么回滚

| 层 | 回滚 |
|---|---|
| 本次改动 | `Copy-Item cordis.patch.yml.bak-20261016-defaultinput cordis.patch.yml -Force`（同目录，改动前备份） |
| 或按行回滚 | 删掉 `defaultInput: ["text", "image"]` 与其上 5 行注释；恢复 `displayName: YG Gateway`（若确实要那个反引号） |

**⚠️ 备份链风险（交接文档已提示，此处给出清单）**：同目录现存
`bak-20260927-123506`(1.4K) / `bak-before-account`(8.8K) / `bak-20261005`(8.8K) /
`bak-wbc-20261009-164325`(9.9K) / `bak-img-20261016`(12.7K) / `bak-20261016-defaultinput`(13.7K)。
**任何一次「回滚到旧备份」都会静默抹掉图片修复**（越旧的备份越早于该修复）。
防取证手段：本目录的 `cordis.patch.yml.snapshot`（与源文件 SHA256 一致：`A61C16D7E2277DB5…`），
差异一 diff 就能看出丢了什么。**建议**：把 `bak-20260927-*` 与 `bak-before-account` 移出该目录归档，或统一加 `.archive/` 子目录。

## 五、收尾（2026-10-16 晚补记）

1. **重启已生效且用户实测通过**：dsh 于 **17:37:42** 重启并重写 `cordis.yml`；`cordis.patch.yml`
   与入库快照 SHA256 一致（`A61C16D7…`）→ 两处改动都在。**用户实测：dsh 里发图可用**
   （属用户观察；网关 reqlog 无法分辨那一次请求——它不含请求体，见 §三.4）。
2. **备份链已收敛（移动而非删除，可逆）**：早于本次修复的 4 个旧备份
   （`bak-20260927-123506`、`bak-before-account`、`bak-20261005`、`bak-wbc-20261009-164325`）
   已移入同目录 `.bak-archive/`，并在其中放了 `README.txt`，写明「不要用它们覆盖现网配置」及其原因
   （会抹掉 `defaultInput` 与 `displayName` 修复）与本仓快照的路径。
   现网目录只留 `bak-img-20261016`、`bak-20261016-defaultinput` 两个（都**晚于或等于**图片修复）。
3. 仍需注意：`.bak-archive/` 里的文件是**历史**，不是可用回滚点；真要回滚请用
   `bak-20261016-defaultinput` 或本目录快照。

## 六、遗留（已知、未做）

1. **需重启 dsh 生效**：`cordis.patch.yml` 是启动期 patch 层；本次改动**尚未重启验证**（本会话运行在 dsh 内，
   不擅自重启会话宿主）。重启后可用「发图 → 模型描述图片内容」复核，与 §三.2 同法。
2. **`cn:auto` 的 `supports_images=true` 是网关目录的声明**：其自身 provenance 未验证
   （按本仓四态纪律，这属 declaration，不等于 observation）。实测只覆盖了 `cn:auto` 落到的那两个模型。
3. **20 条 per-model 声明未逐条实测**：现有证据是「目录声明全为 true」+「2 次落地实测」。
   若要逐条实测需 20 次带图调用；按模型记录实测结果的表可挂在本目录，但目前只有 2 行。
4. **未清理备份链**（§四）：涉及删用户文件，等拍板。
