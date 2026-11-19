# 生产 v2.3.0 真实上游回归：3 条假红的根因与修复

日期：2026-11-19
提交：`<待填>`
上一提交：`70843a9`
改动文件：`scripts/yg-regression-test.py`（**只改脚本，未改服务端**）

## 一、这批的价值

此前所有端到端验证都是 **fake upstream** 或**空池**。生产部署 v2.3.0 后有 2 个健康账号，
这是第一次能对**真实上游**跑完整回归。

**首轮结果：PASS 11 / FAIL 3，exit=1。** 三条 FAIL 全部是**假红** —— 但它们会掩盖真红，
所以必须修。

## 二、三条 FAIL 的根因

### 2.1 `config guardrails 500k/300k`（S1）

```python
check("config guardrails 500k/300k",
      c["guardrails"].get("context_max_tokens") == 500000 and ... == 300000, ...)
```

生产实际是 `max=1000000 warn=600000`。**脚本断言的是一个快照值，不是不变式。**

### 2.2 `413 + context_length_exceeded`（S3）

```python
big = "测" * 520000   # 写死，假设 max=500k
```

配置调到 1M 后，520k 字符**根本不越界** → 正确放行 → 200 → 断言失败。

**注意**：这一条是 2.1 的**下游后果**，同一个根因。

### 2.3 `坏 auth → 401`（S5.1）—— 这条不是配置问题

现象：`status=-1`。而 PowerShell 直接打同一个坏 key，**稳定返回 401**。

复现序列（脚本化的）：

| 步 | 请求 | 结果 |
|---|---|---|
| 1 | 好 key + 免费模型 | 200 |
| 2 | 坏 key | **401** |
| 3 | 再打一次坏 key | **`ConnectionResetError(10054)`** |

**机制**：服务端在鉴权失败时**未读请求体就返回 401**，Go 因此发送
`Connection: close`。客户端若复用这条已关闭的连接，下一次写入就吃 RST。
`urllib` 不自动重试 RST，于是 `request()` 落到 `except Exception` → 返回 `-1`。

**这不是服务端缺陷**：
- 拒绝时立刻断开是防御性行为（不为一个没有合法凭证的客户端读取 up to 64MB body）
- PowerShell / curl 每次新建连接，永远正常
- 只有复用连接的客户端会撞上

**所以修的是脚本，不是服务端。**

## 三、三处修法

| 处 | 改法 | 为什么不是打补丁 |
|---|---|---|
| S1 | 断言**不变式**：`0 < warn < max` | `warn >= max` 会让警告永不触发，那才是真缺陷；具体数值随配置变，不该钉死 |
| S3 | 从 config 读 `max`，构造 `max*1.05+20000` 字符；超过 2M 则跳过并说明 | 边界必须跟着配置走。加 2M 上限是因为超长文本本身要几 MB，而护栏是逐字计数的确定性路径，不值得为一个配置值付这个代价 |
| S5.1 | 显式 `Connection: close` | 消除连接复用带来的不确定性；不去给 `request()` 加「RST 自动重试」，那会掩盖真实网络故障 |

**反记账纪律核对**：
- 没有放宽任何判据。S3 的 413 断言、S5.1 的 `== 401` 断言都**原样保留**
- 没有删任何用例。S1 的检查项数不变，只换判据形式
- 跳过（2M 上限）会**打印理由**，不计为 PASS

## 四、修复后实测（真实上游，零积分）

```
== S1 配置自检 ==
  [PASS] config guardrails 合法（warn < max 且均 > 0） max=1000000 warn=600000
== S2 健康基线 20 次 ==
  [PASS] 100% 200 20/20   prompt_tokens stdev=0.00   耗时 avg=8.83s p95=21.44s
== S3 护栏 max 边界（配置 max=1000000，构造 ~1070000 字符）==
  [PASS] 413 + context_length_exceeded status=413 code=context_length_exceeded dt=0.1s
  msg=estimated prompt tokens 1070001 exceed guardrail max 1000000
== S5 无效输入族 ==
  [PASS] 坏 auth → 401 status=401
  [PASS] 畸形 JSON → 4xx status=400
  [PASS] 不存在模型 → 4xx 或冷却 503 status=503 code=no_healthy_account
  [PASS] 空 messages append 补全后透传 status=200
  [PASS] 空 body → 4xx status=400
== S6 超大 body ==
  [PASS] 超大 body → 413 status=413 code=request_too_large dt=0.1s
== S7 流式 SSE 完整性 5 次 ==
  [PASS] 5/5 SSE 完整 5/5

PASS 14 | FAIL 0   全部通过   exit=0
```

**两条 `dt=0.1s` 是强证据**：1.07M 字符的护栏拦截、>64MB body 拒绝，都在 0.1 秒返回 ——
证明**确实在本地拦下、没打到上游**，符合两条断言注释里写的「本地拦截」。
`/status guardrail_stats={"blocked_hits": 1, ...}` 也印证了拦截计数。

## 五、这批同时验证了什么

**生产 v2.3.0 在真实上游下工作正常**：
- 20 次非流式全部 200，`prompt_tokens` 完全稳定（stdev 0.00）—— 选号与提示词拼接没有抖动
- 5 次流式全部以 `data: [DONE]` 收尾 —— 流式没被本次改动破坏
- 坏 auth / 畸形 JSON / 空 body / 超大 body 四条拒绝路径全部符合预期

## 六、能力边界

1. **S4（会话 RPM 限流）与 S8（并发）仍跳过** —— S4 按它自己的注释「故意打上游冷却」，
   S8 会在冷却期产生噪声。两者都不是本批目标。
2. **只有 `cn` realm 被验**，`global` 不可服务（`realm_servable.global=false`）。
3. 未验全部 17 个模型，只用 `cn:hy3 ×0`。
4. 生产上的**流式失败**仍未验证（需要真实上游失败，不可控）。

## 七、回滚

```powershell
git revert --no-edit <本批 sha>
```
仅影响回归脚本，不影响生产二进制。
