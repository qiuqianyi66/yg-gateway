# 2026-10-24 仓库卫生 + 公网上传安全复核

## 现状态（2026-10-24 第二轮复核后修正）

**上一轮结论「全仓无凭证」是错的，已作废。** 根因写在下面「误判复盘」一节。

## 改了什么

### 第一轮（commit 46aef77）

1. `.gitignore` 新增 4 条排除本机产物：`/.workbuddy/`、`/yg-gateway-worktrees/`、
   `/draft_*_folder/`、`/docs/research/_shots/`。
2. 归档 `docs/research/*.html` —— **第二轮已撤回**（属私有策略材料，见下）。

### 第二轮（本提交）

从版本控制中移除 6 个**个人私有资产**（本地文件保留，只取消跟踪 + 入忽略表）：

| 文件 | 为什么不能公开 |
|---|---|
| `image_edited.png` | 聊天截图，**画面内是明文 API Key**；且是私人对话内容 |
| `HANDOFF.md` | 项目交接件：私有实例路径、账号数量、persona 存在性、部署细节 |
| `工作准则-融入版.md` | 个人工作准则 |
| `yg-gateway_四维度审查收口_任务交接.md` | 个人任务交接件 |
| `yg-gateway_四维度审查收口_对账.json` | 同上 |
| `docs/research/YG-Gateway深度研究与产品PRD-v2.html` | 产品研究/PRD 草稿，私有策略材料 |

## 误判复盘（真因：二进制盲区）

第一轮的「全历史凭证扫描」用的是 `git grep -I`。**`-I` 会跳过二进制文件**，
所以 PNG 里的像素从来没被读过。文本 296 提交零命中 → 被误报成「无凭证」。

真正的凭证就在一张 **PNG 截图**里：`image_edited.png`（2026-10-12 由 `f1e6324` 引入，
2026-10-24 发现，公开约 12 天）。肉眼一读就看到了。

**教训（与 L-15「否定性断言必须穷尽证据源」同源）**：凭证扫描必须覆盖
**文本 + 图片 + 二进制**；`-I` 不是「没问题的证明」，是「没检查」。图片一律人工过目。

## 暴露面清单（origin/main @ 46aef77 实测）

| 类别 | 内容 | 严重度 |
|---|---|---|
| **明文凭证** | `image_edited.png` 内 `[REDACTED-KEY]`；经核对该 key **仍在 `D:\yg-gateway\config.json`**（生产实例 `api_keys[0]`） | **高** |
| 真实账号昵称 | `.harness/changes/2026-10-18-b1b-panel-unknown-visual/*.png`、`docs/screenshots/*.png` 里出现真实账号昵称与 UUID 前 12 位 | 中 |
| 用户名 / 机器名 | `<local>`、`<domain>\<user>`、`owner: maintainer`，散见 11 个 `.harness/changes/**` 与 `docs/PLAN-*.md` | 低 |

## 缓解（按顺序，第 1 条最急）

1. **轮换 key**（唯一能真正止损的动作）：`D:\yg-gateway\config.json` 的 `api_keys[0]`
   换新值 → `scripts/deploy-prod.ps1` 重启 → 冒烟。（当前 `listen=127.0.0.1:7863`，
   外网不可直连，故不是「立刻被打」级别，但 key 已公开，必须视为已泄露。）
2. **历史清除**（可选，需强推）：`image_edited.png` 与 `docs/research/*.html` 仍留在
   历史里，`git log` 可取出。彻底清除要用 `git filter-repo` 重写 + `push --force-with-lease`。
   **未执行**：破坏性操作需单独确认，且 force-push 会让已有 clone/fork 分叉。
3. 本提交只做「不再新增暴露」：HEAD 已移除上述 6 文件 + 忽略表拦截。

## 怎么验证

```powershell
git ls-files | Select-String 'image_edited|HANDOFF\.md|工作准则|四维度|docs/research'   # 期望空
git check-ignore -v image_edited.png docs/research/x.html HANDOFF.md                    # 期望全部命中
powershell -NoProfile -File scripts/pre-commit.ps1 -Fast                                # 期望退出码 0
```

## 怎么回滚

```powershell
git revert <本提交>     # 恢复跟踪（不会恢复公网历史，历史清除需 filter-repo）
```

## 边界

- 本轮图片只人工过目了 5 张 PNG（全仓已无其它图片）。
- 历史清除**未做**：因此 `image_edited.png` 里的 key 在 GitHub 历史中仍可取，
  只有「轮换 key」能真正解除风险。
- `.harness/changes/**` 里的用户名字样**未处理**：删掉会掏空审计链（本项目对外承诺的
  可审计性），且重写 80+ 历史文件风险高于收益。属可接受的残留，明确记录在此。

---

# 第二轮处置（2026-10-24 17:xx 执行记录）

## 已完成

| # | 动作 | 证据 |
|---|---|---|
| 1 | **轮换泄露的 API key** | `D:\yg-gateway\config.json` 顶层 `api_key` 换新（备份 `config.json.bak-20261024-keyrotate`）；新值存 `D:\yg-gateway\NEW-API-KEY-20261024.txt`（ACL 仅本机用户可读）；实例重启 PID 39804（cmd 重定向启动，deploy-prod.ps1 同款） |
| 2 | 切换提交邮箱 | `git config user.email qiuqianyi66@users.noreply.github.com`（local 作用域，只影响以后提交） |
| 3 | 测试里的真实 uid 换假值 | `internal/panel/{coverage_handlers_test.go,security_test.go}`：`248890d9-…` → `11111111-2222-4333-8444-555555555555`；`go test ./internal/panel` ok |
| 4 | 删除本地恢复副本 | `yg-gateway-FULL-BACKUP-20261024.bundle`（含旧历史）；被移除文档的本地原件仍在磁盘，仅 git 元数据不可恢复 |
| 5 | 删除本地泄漏截图 | `image_edited.png` 已删（未被跟踪、无引用） |

## 冒烟（换 key 后）

| 项 | 结果 |
|---|---|
| `GET /healthz` | 200，`healthy:4/4`，`realm_servable.cn:true` |
| `GET /v1/models`（新 key） | 200，21 个模型 |
| `GET /v1/models`（旧 key） | **401**（证明轮换生效） |
| `POST /v1/chat/completions`（非流式） | 200，usage in=208 / out=8 |
| 同上（`stream:true`） | 200，11 个 SSE 事件 |

## 按 R8 保留、未改动

`internal/upstream/school.go:81` 的 `machineId` 与同 payload 的 `os/osVersion/arch/timezone` 属**上游协议数据**（AGENTS.md R8：改名有风控风险，一律拒绝并说明）。它是设备指纹字段，不是凭证；改为新 UUID 会让同一批账号的设备画像突变，对养号有实际风险。**保留原值**，记录在此。

## 审查发现（本轮新增，已处理状态）

| 发现 | 状态 |
|---|---|
| 提交身份邮箱 `3541864988@qq.com` 公开在每次提交里 | **已改**以后提交为 noreply；**历史作者仍含旧邮箱**，只能靠重写历史清（若最终删库重建可顺带 `--mailmap`） |
| 测试夹具里的真实 uid | 已换假值 |
| `school.go` machineId | 按 R8 保留 |
| 仓库根 `image_edited.png` | 已删本地 + 已从 main/tag 历史移除 |
| 旧 SHA 仍可取回该图 | **待 GitHub Support 清缓存**（工单已由用户提交） |

## 待办

1. GitHub Support 工单结果（清 unreachable objects + `refs/pull/1/head`）
2. 本轮 commit **只在本地**，未推送（保持远端 HEAD = `b55ae8b`，与工单描述一致）；工单结束后推送
3. 桌面 `yg-release-backup`（19MB，6 个 release 附件）待删库重建后重传，传完可删