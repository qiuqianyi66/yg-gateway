# 真实 Anthropic SDK 消费流式错误帧

日期：2026-11-19
提交：`<待填>`
上一提交：`ee116c1`
探针脚本：`probe.py`（同目录，可复现）

## 一、这批消解的是什么

`.harness/changes/2026-11-19-live-instance-verify/summary.md` 里明确留了一条未消解：

> 仍未用真实 Anthropic SDK / Claude Code 联调。它们能否把流内 `error` 事件正确
> 弹给用户，仍未验证。

这条是 `d03b52e`（流中途错误帧）的**最后一环**：网关把错误送出去了，
但如果客户端 SDK 把它当正常结束吞掉，等于白修。

## 二、方法

- SDK：`anthropic` Python **2.10.0**（装在 `%TEMP%\anthropic-sdk` 的临时 venv，未污染系统 Python）
- 实例：本树构建的空池沙箱（`WB2A_AUTH_DIR` 指向空目录，`/healthz` 返回 `total:0`）
- 端口 `:17865`，与生产 7863 / e2e 7864 隔离
- **零上游调用、零积分消耗**

## 三、三条结果

```
[缺model]           抛出 TypeError: Missing required arguments; Expected either
                    ('max_tokens','messages' and 'model') ...
[非流式+空池]        抛出 ServiceUnavailableError: Error code: 503 -
                    {'error': {'code': 'no_healthy_account', ...}}
[流式+空池]          抛出 APIStatusError: {'error': {'message':
                    'no_healthy_account: all accounts are temporarily unavailable...',
                    'type': 'upstream_error'}, 'type': 'error'}
```

## 四、核心结论：流式错误**不会被吞**

第三条最重要。网关在流里发的是：

```
event: message_start
event: content_block_start
event: error            ← 网关的这一帧
```

SDK 收到后**抛 `APIStatusError`**，而不是把流当正常结束。

**对照网关修复前的行为**（`addde41` 记录里的改前实测）：

```
message_start / content_block_start / content_block_stop /
message_delta(stop_reason=end_turn) / message_stop
```

在这种输出下，SDK 会**正常**走到 `get_final_message()`，拿到
`stop_reason="end_turn"` —— 客户端以为模型答完了，**错误被完全吞掉、不报错**。

所以 `d03b52e` / `addde41` 的价值在这一步才算闭环：
**网关送出可见错误 → 真实 SDK 把它变成异常 → 调用方必然知道**。

## 五、一个反向发现：缺 model 的校验被 SDK 抢先

第一条：SDK **客户端侧**就要求 `model` 必填，缺了直接 `TypeError`，
**根本到不了网关**。

含义：
- 网关的 `/v1/messages` 缺 model → 400 校验，对**用 SDK 的调用方**永远是死路
- 但它对 **curl / 自研客户端 / 手写 HTTP** 仍然有效 —— 那些才是真正会静默降级的场景
- 所以这条修复的价值面比"所有客户端"窄，但**没有失效**，只是被 SDK 天然屏蔽

诚实标注：**不能因为 SDK 挡住了就说这个修复没用**。它保护的是不走 SDK 的调用方。

## 六、能力边界

| 项 | 状态 |
|---|---|
| SDK 能否收到流内错误并抛异常 | **已验证**（SDK 2.10.0） |
| Claude Code（CLI 应用）行为 | **未验证**（需装 CLI，且要真实上游才跑得起来） |
| 上游真发 error 帧的场景 | **仍未验证真实上游**，只有 fake upstream 证据 |
| 生产实例上的流式失败 | **未验证**（需真实上游失败，不可控） |
| 其他 SDK 语言（Node/Go/Java） | **未验证** |

## 七、为什么要归档 probe.py

不是一次性的：空池实例 + 任意 SDK 版本组合都能复跑，是**可复现的验证工具**，
不是调试残留。任何人质疑"SDK 到底会不会抛"都能直接跑一遍。

需要的前置：

```powershell
# 空池实例（见 2026-11-19-live-instance-verify）
python -m venv %TEMP%\anthropic-sdk
%TEMP%\anthropic-sdk\Scripts\pip install anthropic
%TEMP%\anthropic-sdk\Scripts\python probe.py
```

## 八、回滚

```powershell
git revert --no-edit <本批 sha>
```
