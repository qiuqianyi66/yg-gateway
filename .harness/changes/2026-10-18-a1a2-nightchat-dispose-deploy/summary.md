# 生产换版记录 — A-1/A-2（夜猫子末次等待 + 达标未领统一去领奖）

- 日期：2026-10-18 21:55:59
- 脚本：`scripts/deploy-prod.ps1 -Build -Feature a1a2-nightchat-dispose`
- 退出码：**0**（成功，含冒烟通过）
- 报告：`%TEMP%\deploy-prod-20261018-215559.json`；stderr：`%TEMP%\yg-gateway-20261018-215559.err.log`

## 一、换版事实

| 项 | 值 |
|---|---|
| 源码 | `git_head=bcba9bf`，`git_dirty=false`（脏文件 0） |
| 旧 exe | `0E10FA1F17929030…`（`6251ecb` 构建），PID 40588 |
| 新 exe | `A48F2CCAC658B584…`（`bcba9bf` 构建），PID **11456** |
| 备份 | `D:\yg-gateway\yg-gateway.exe.pre-a1a2-nightchat-dispose-20261018.bak`（hash = 旧 exe） |
| 健康 | `/healthz` 200，`healthy=3/4`（未健康仍是 global 账号 `realm_servable.global=false`，与本轮无关） |
| 孤儿 launcher | 回收 1 个（PID 32640，父进程 8480 已不存在） |
| 用时 | 20.5s（含 go build） |

## 二、冒烟（脚本内建，3/3）

| 项 | 结果 |
|---|---|
| `/v1/models` | 200，20 个模型 |
| 非流式（`cn:auto`） | content 0 / reasoning 56 字符，`finish_reason=length` |
| 流式 | HTTP 200，`data` 增量帧 True，`[DONE]` True |

## 三、本次上线带来的行为变化（用户可感知）

1. **夜猫子（B4a）**：每次任务少一次尾部 4s 等待；对话之间的 4s 间隔不变（风控口径未动）。
2. **达标未领（A7/D3）**：「一键完成全部可自动任务」会**开始领取**此前被跳过的奖励；
   队列路径对达标未领项不再先跑一次动作，直接领奖。
   → 可观测变化：面板任务结果里会出现 `claimed=true / credit / energy`，以及文案「已达标未领：已领奖 +N 分 +M 能」。

## 四、回滚

```powershell
powershell -NoProfile -File scripts/deploy-prod.ps1 -Exe "D:\yg-gateway\yg-gateway.exe.pre-a1a2-nightchat-dispose-20261018.bak" -Feature rollback-a1a2
```

或代码层 `git revert c8968c0 444376d` 后 `-Build` 重发。备份 hash 与旧 exe 一致（报告 `backup_hash` 已核对）。

## 五、同轮另做的磁盘清理（D4，用户批准）

`D:\yg-gateway` 的 `*.bak`：**45 份 / 452.3 MB → 2 份 / 20.4 MB**（删 43 份，回收 **431.9 MB**），
保留 `pre-adr009-010-guards-20261018`（`FDF92DE1…`）与 `pre-pg-dropdown-multiplier-20261017-192048`（`E7A676DE…`），
删除前的完整 sha256 清单在 `D:\yg-gateway-backups-sha256-20261018.txt`。
本次换版又新增 1 份备份（`pre-a1a2-…`），当前共 **3 份 / 30.6 MB**。
