# 生产部署 v2.3.0：77 个提交生效

日期：2026-11-19
计划：`docs/plans/2026-11-19-deploy-and-closeout.md`（门 1，已批准）
tag：`v2.3.0`
部署报告：`D:\yg-gateway\data\logs\deploy-prod-20261119-112104.json`

## 一、门 1 的批准与前置

用户批准：**「先跑排练，通过后直接部署」**。这是明确决策，不是「继续」。

## 二、门 1.5：排练（沙箱，不碰生产）

`scripts/deploy-prod.rehearsal.ps1`，在 `%TEMP%` 沙箱、端口 7864：

```
排练全部通过（执行断言 47 项，0 失败）
```

**场景 E（-Build + 脏工作区拒绝）按设计跳过** —— 脚本自己说明「当前工作区是干净的：
-Build 的拒绝路径无法测（不为了测它去弄脏仓库）」。这是脚本的正确取舍，不是漏测。

覆盖到的风险场景：
- A1 非 PE 被预检拒绝且生产零触碰
- A2 坏 PE 自动回滚
- B 替换生效且不拖死调用方
- C `-DryRun` 无副作用
- **D 停服后替换失败也把旧版拉回来**（这条是回滚链的牙齿）

## 三、门 5：部署（六步，全绿）

工作区干净（脏文件数 0），执行：

```
deploy-prod.ps1 -Build -Feature deploy-2.3.0
```

| 步 | 结果 |
|---|---|
| 1/6 备份 | `yg-gateway.exe.pre-deploy-2.3.0-20261119.bak`，sha256 `344CBE7E…` |
| 2/6 停旧实例 | pid 10296 停止，端口 7863 已释放 |
| 3/6 替换 | sha256 `76DBDB03…` |
| 4/6 启动 | stderr 独立文件 |
| 5/6 健康轮询 | HTTP 200 `healthy=2/4` |
| 6/6 冒烟 | `/v1/models` 200 / 17 模型；非流式 ok；流式 `[DONE]`=True |

**退出码 0**，用时 7.6s。

## 四、独立复查（不信脚本自报）

### 4.1 哈希三方对齐

| 对象 | 实测 | 与报告 |
|---|---|---|
| 磁盘 `D:\yg-gateway\yg-gateway.exe` | `76DBDB03…AFFB6F9` | **= after** ✓ |
| 备份 `.pre-deploy-2.3.0-20261119.bak` | `344CBE7E…44887563` | **= before** ✓ |
| 备份文件存在 | True | 可回滚 ✓ |

### 4.2 生产 `/healthz`（免鉴权）

```json
{"healthy":2,"realm_servable":{"cn":true,"global":false},"service":"yg-gateway","total":4}
```

与部署脚本报的 `healthy=2/4` 一致。

### 4.3 行为探针（验证修复真的在生产生效）

**探针 1 — anthropic 缺 model（`1d7a895` 的修复）**

```
POST http://127.0.0.1:7863/v1/messages  {"max_tokens":64,"messages":[...]}
=> HTTP 400
{"error":{"code":"invalid_request","message":"transform: model 必填：…"}}
```

改前是 HTTP 200 + 正常回答。

**探针 2 — 非法 JSON**

```
=> HTTP 400  {"error":{"code":"invalid_request","message":"transform: unexpected end of JSON input"}}
```

**探针 3 — `/v1/chat/completions` 缺 model**

```
=> HTTP 400
{"error":{"code":"model_not_found","gateway_hint":"upstream has no such model on this backend…",
 "message":"{\"code\":11102,\"msg\":\"model [] service info not …"}}
```

## 五、一处我自己的疏忽（必须记）

探针 3 我原以为会和 anthropic 侧一样**在本地被拒**，因此宣称这一批"零上游调用"。
实测不是：`/v1/chat/completions` **没有**本地空 model 校验，它选了默认模型打到上游，
由上游以 11102 拒掉。

- 代价：一次在受理前被拒的模型查询，通常不计费
- 性质：**探针设计疏忽**，不是缺陷 —— chat 侧不校验空 model 是既有行为，
  与 anthropic 侧的严格校验不同
- 教训：**不要把「我以为会走本地分支」当成「它就是走本地分支」**；
  打生产上游前先确认该路径是否真的不打上游（L-22 的具体一例）

## 六、部署后状态

- 生产 `D:\yg-gateway`，端口 7863，运行 `v2.3.0`（`76DBDB03`）
- 备份保留：`yg-gateway.exe.pre-deploy-2.3.0-20261119.bak`（`344CBE7E`）
- 账号健康 2/4，`cn` 可服务、`global` 不可

**回滚路径**：

```powershell
# 停新实例 → 恢复备份 → 启动
# 或直接用 deploy-prod.ps1 重新部署旧版
```

## 七、能力边界（不宣称的部分）

1. **流式失败可见性未在生产验证**：生产有健康账号，造流内失败需要真打上游并让它报错，
   不可控。该修复由空池沙箱实例（:17865）验证过，证据在
   `.harness/changes/2026-11-19-live-instance-verify/summary.md`。
2. **未做真实 Anthropic SDK 联调**（用户已批准作为下一批）。
3. 冒烟用的是 `cn:step-5-preview`，未覆盖全部 17 个模型。

## 八、回滚

代码侧：`git revert --no-edit c3906f9`（仅计划文件，无代码影响）
二进制侧：恢复备份文件（见第六节）
tag：`git tag -d v2.3.0 && git push origin :refs/tags/v2.3.0`
