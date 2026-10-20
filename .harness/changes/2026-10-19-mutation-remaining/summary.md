# 变异核验 — 收掉最后四条 unknown（2026-10-19 第四批）

- 日期：2026-10-19
- 分级：**L1**（清单 + 三处测试 + 两处工具加固；产品代码未改）
- 新增清单：`upstream-expert-chat`（6）+ `pool-ewma-latency`（5）
- 对应上一轮收口时列的四条 unknown

## 一、unknown ①：`DesktopChatWithExpert` 真实 chat 链路

新清单 `upstream-expert-chat`（`internal/upstream/desktop.go`：

| id | 变异 | 首轮 |
|---|---|---|
| XC1 | 不发 `X-Expert-Id` | 咬住 |
| XC2 | 非 200 判据放宽成 `>=400`（3xx 也当成功） | **存活** |
| XC3 | requestId 形状校验去掉 | 咬住 |
| XC4 | R8：`conversationId` 前缀 `wb2api-conv-` 被改 | 咬住 |
| XC5 | `stream_options.include_usage` 去掉 | **存活** |
| XC6 | 搜索偏移不推进（反复命中同一位置） | 咬住 |

存活两条的实质：

- **XC2**：既有用例只测了 500，没测 3xx。放宽成 `>=400` 后 302 会被当成功，解析器读空 body，
  最终报「SSE 中未找到 requestId」——**错误形态掩盖真实原因**。
  补 `TestUpstreamHTTPDesktopChatWithExpertRedirectIsError`：302 必须报 `chat http 302`。
- **XC5**：`include_usage` 决定上游回不回 usage，去掉 = 成本观测静默断流；原来没人断言请求体。
  在既有 `TestUpstreamHTTPDesktopChatWithExpert` 里补断言。

→ 补测试后 **6/6 咬住**。

## 二、unknown ②：`RecordTokenUsage` 延迟样本长序列

新清单 `pool-ewma-latency`（`internal/pool/entry.go` + `state.go`）：

| id | 变异 | 首轮 |
|---|---|---|
| L1 | 首个延迟观测不再直接赋值 | 咬住 |
| L2 | 延迟样本数不累加 | 咬住 |
| L3 | `RecordTokenUsage` 不喂延迟 EWMA | 咬住 |
| L4 | `>= 0` 写成 `> 0`（0ms 观测被丢） | **存活** |
| L5 | `LastLatencyMs` 写 0 | 咬住 |

**L4**：0ms 是合法观测（本地回环、缓存命中），此前没有任何用例喂 `LatencyMs: 0`。
补 `TestEwmaLatencyZeroMillisecondRecorded`（样本数必须 +1，EWMA 为 0）。
→ 补测试后 **5/5 咬住**。

## 三、unknown ③：`(cached) ⇒ 无效变异` 判据实弹

构造真实缓存命中：复制 `mutation-check.ps1` 并**去掉 `-count=1`**，用一条只改
`internal/panel/autotask.go` 注释的探针变异、却测 `internal/auth` 包——测试输入未变，
`go test` 必然命中缓存：

```
[CACHE1] 无效变异  改一个与 internal/auth 无关的文件… | 构建缓存命中 —— 变异未进编译
包 internal/auth：咬住 0 / 存活 0 / 无效 1     exit 1
```

判据按预期把它读成**无效变异**（而不是「存活」）。这条 unknown 关闭。
（副产物：确认了「无关文件 + 缓存」是构造假绿的通用手法，正是 L-17 那个坑的形态。）

## 四、unknown ④：`%TEMP%` 副本被清理

上一轮已加「副本完整性自检 + 自动重建 + 失败时打印子输出」；本轮再消掉根因面：

- `run-all.ps1` 的**默认副本位置从 `%TEMP%\mut-all` 改到 `%LOCALAPPDATA%\yg-mut\all`**
  （`%TEMP%` 会被清理任务扫，长跑 60+ 分钟的副本放在那里等于把证据放在会被删的地方）；
- 仍保留 `-Clone` 覆盖与 `-Fresh` 重建。

验证：新默认路径下 `-Only h3` 正常克隆并跑完（`咬住 1 / 存活 0 / 无效 0`）。

> 为什么不是「查明是谁删的」：本机 `%TEMP%` 清理策略不在本仓可控范围，取证成本高于收益；
> 把它变成「不依赖 `%TEMP%`」是更便宜的等效手段（AGENTS.md 阶段 0 的判据④）。

## 五、复现

```powershell
powershell -NoProfile -File scripts/mutation/run-all.ps1 -Fresh          # 18 清单全量
powershell -NoProfile -File scripts/mutation/run-all.ps1 -Only expert   # 专家对话链
powershell -NoProfile -File scripts/mutation/run-all.ps1 -Only latency  # 延迟 EWMA
```

## 六、四条 unknown 的现状

| unknown | 现状 |
|---|---|
| ① `DesktopChatWithExpert` chat 链路 | **已进清单（6/6）** |
| ② `RecordTokenUsage` 延迟长序列 | **已进清单（5/5）** |
| ③ `(cached)` 判据未实弹 | **已实弹验证**（判无效变异，exit 1） |
| ④ `%TEMP%` 副本被清理 | **已缓解**（默认副本移出 `%TEMP%` + 完整性自检）；清理者本身仍未定位（不再影响结论） |
