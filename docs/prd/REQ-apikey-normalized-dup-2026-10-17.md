# REQ — 顶层 api_key 与多 Key 的重复检测必须同口径（B3-c）

- 状态：已确认（用户 2026-10-17 批准继续处理复审登记的三条缺口）
- 日期：2026-10-17
- 分级：**L2**（改的是配置校验行为：原本放行的配置现在 fail fast）
- 来源：独立复审自选变异 **B3-c**（`docs/PLAN-open-items-2026-10-17.md` B3 行）

## 一、必要性

`normalizeAPIKeys` 有两条「同一密钥不许出现两次」的拦截：与顶层 `api_key` 重复、与已见多 Key 重复。
第二条用 `seenKey` 映射，键是 **TrimSpace 后**的 `key`；第一条却拿未归一化的 `c.APIKey` 去比：

```go
if c.APIKey != "" && key == c.APIKey {   // key 已 trim，c.APIKey 从未 trim
```

于是 `api_key: " sk-dup "` 配 `api_keys[].key: "sk-dup"` 会被判成「不同密钥」而放行。
而 `api_keys[].key` 会被 TrimSpace 后落库（`k.Key = key`），顶层 `api_key` 的运行时摘要直接取原值
（`main.go` 的 `sha256.Sum256([]byte(c.APIKey))`）——**管理员意图里的同一把密钥静默变成两把**：
约束是否生效取决于客户端发的是哪一个，排查时也看不出这是同一个值。

**不做会怎样**：这类配置错误不会有任何提示，只会表现为「某把 Key 的 models/realm 约束没生效」，
属于最难查的一类（不报错、不记录，只是行为不同）。而且现有测试全部用「逐字相同」的 key，
`git grep` 也找不到带空格的用例，说明这块从没被覆盖。

**为什么值得修而不是只补测试**：这是**一行比较口径**的问题，修完行为唯一且可解释；
只补测试等于把一个已知会被踩的坑写成「已记录」。

## 二、目标与非目标

**目标**：两条重复检测用**同一口径**（TrimSpace）判断「是否同一把密钥」。

**非目标**：

- **不改鉴权语义**：运行时摘要仍用 `c.APIKey` 原值，不 trim 落盘值、不改 `httpauth` 的比对；
- 不改多 Key 的其它校验（id 合法性、realm、models）；
- 不新增配置项、不写迁移。

## 三、验收（Given-When-Then）

**F-B3c-a 带空格的同值必须被拒**
- Given `api_key: " sk-dup "` 且 `api_keys[].key: "sk-dup"`（或反向：顶层无空格、多 Key 带空格）
- When 单次 normalize（`ParseConfig`，面板保存路径）
- Then **返回错误**（fail fast），错误信息指向「与顶层 api_key 重复」

**F-B3c-b 不同值不受影响**
- Given 顶层 `api_key` 与多 Key 去掉空格后仍不同
- Then 照常通过（既有 `TestAPIKeysSingleAndMultiCoexist` 等用例必须继续绿）

**F-B3c-c 兼容既有入口**
- Given 走 `Load()`（会 normalize 两遍）
- Then 行为一致（不因两次 normalize 而改变判定）

## 四、影响分析

| 面 | 影响 |
|---|---|
| 配置行为 | **变严**：一类此前静默放行的配置现在 fail fast（正是本条目的） |
| 鉴权语义 | 不变（运行时摘要仍取原值） |
| 兼容性 | 只影响「顶层 api_key 与某条多 Key 归一化后同值」的配置——这种配置本身就是误配 |
| 证据 | 新测试在**未修复代码**上红（副本实测），修复后绿；见 summary §三 |