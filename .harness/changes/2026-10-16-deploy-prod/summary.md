# 变更记录 — 换版流程固化为脚本（N1）

- 日期：2026-10-16
- 需求：`docs/prd/REQ-deploy-prod-script-2026-10-16.md`
- 计划：`docs/superpowers/plans/HANDOFF-2026-10-16-ci-closed.md` §3 N1（决策 D3 = 现在做）
- 提交：见本节末「提交清单」
- 级别：L2（新增两个 `scripts/` 下的工具脚本，不改生产代码路径，不改依赖方向）

## 一、改了什么

**新增两个文件，没有改动任何既有文件。**

| 文件 | 行数 | 作用 |
|---|---|---|
| `scripts/deploy-prod.ps1` | 618 | 生产换版一条命令：备份（R15）→ 等端口释放 → PE 预检/替换 → 启动（cmd 重定向落 stderr）→ 健康轮询 → 冒烟 → 失败自动回滚；每步落 JSON 报告 |
| `scripts/deploy-prod.rehearsal.ps1` | 359 | 上述脚本的排练：在 `%TEMP%` 沙箱（自己一份 config/auths(空)/data、端口 7864）跑 4 个场景，41 项断言，**不碰生产** |

`AGENTS.md` 的部署纪律原本是「替换前备份 / 构建复制启动同一条命令 / 部署后冒烟」三条靠人记；
现在前两条由脚本执行、第三条由脚本执行并落报告。

### 1.1 为什么要有排练脚本（而不是只写部署脚本）

换版脚本做的恰恰是「让服务短暂不可用」这件事。**它自己的回滚路径有 bug，危害比没有脚本更大**
——会自信地把生产搞停。所以脚本必须有一个不碰生产的、可反复跑的验收装置。
本轮 4 个真问题里有 3 个是排练跑出来的，不是读代码读出来的（见 §二）。

### 1.2 设计要点（每条都有实测依据，不是偏好）

| 决定 | 依据 |
|---|---|
| 启动借 `cmd.exe` 做重定向，整串**再包一层引号** | (a) `Start-Process -RedirectStandard*` 会让常驻孙子进程持有调用方 stdout 管道写端 → `& powershell -File` 捕获时外层阻塞到该进程退出（实测 8865ms vs 内层 3s）；(b) `cmd /c "exe" ...` 的引号规则会剥掉最外一层引号、把重定向吃掉 → **进程静默不启动**（实测 listeners=0、日志 0 字节、无报错）；包一层后 listeners=1、stderr 1047 字节 |
| 替换/恢复二进制走「写 `*.staged` + `Move-Item -Force`」 | 直接覆盖被占用的目标名会持续失败；改名替换成功（覆盖要写权限，改名只要 FILE_SHARE_DELETE）。实测见 §二.2 |
| 启动前 PE 预检（MZ + PE 签名） | 把非 PE 交给 launcher 会弹模态对话框、卡死 launcher、锁住 exe。预检把这类错误挡在**停服之前** |
| 健康判据 = 「有 HTTP 响应 + JSON 里有 `service:yg-gateway`」+（可选）`healthy >= MinHealthy` | `/healthz` 在 0 账号时返回 **503**（探针实测）；只认 200 会把「起来了但没账号」误判成「没起来」→ 触发不该发生的回滚 |
| HTTP 直连 `System.Net.HttpWebRequest` | PS 5.1 的 `Invoke-WebRequest` 在非 2xx 时**已把 body 读掉**，再读 `Response.GetResponseStream()` 得空串 → 全部健康检查假红（实测一次刷出 14 项假失败） |
| 不设体积下限 | 体积是「拿错文件」的证据，阈值必然武断；加上它会把排练里「合法 PE 但立刻退出」的对照件（`where.exe` 64KB）误杀，导致**真正要紧的「健康超时→回滚」路径不可测** |
| 冒烟失败不回滚 | 冒烟失败多为上游账号/额度问题，回滚会来回折腾；退出码 3 + 打印备份路径，由人判断 |
| 报告在任何退出路径都落盘 | 否则「脚本挂在哪一步」只能靠猜；报告在 `finally` 里写 |

### 1.3 退出码契约

| 码 | 含义 |
|---|---|
| 0 | 部署成功（含冒烟通过；`-SkipSmoke` 时不因冒烟失败） |
| 1 | 前置检查失败 / 停旧实例失败 / 候选非 PE（**生产未被触碰或已恢复原状**） |
| 2 | 新实例健康检查失败 → **已自动回滚**，服务回到旧版 |
| 3 | 部署+健康都过，但**冒烟失败**（不回滚） |
| 4 | 回滚也失败或未预期错误（**生产可能不可用**，看 stderr 日志） |

## 二、怎么验证的

### 2.1 排练（不碰生产）：4 场景 41 项断言，连跑 2 次全绿

```
powershell -NoProfile -File scripts/deploy-prod.rehearsal.ps1     # exit 0
```

| 场景 | 断言数 | 关键结果 |
|---|---|---|
| A1 非 PE（33 字节文本） | 10 | 退出码 1；`stage=rejected-not-pe`；**没有** `.bak`、没有 `.staged`、没有新实例日志；exe 哈希未变；**监听 PID 42020 → 42020（没停服）** |
| A2 合法 PE 但立刻退出（`where.exe`） | 9 | 退出码 2；`rolled_back=true`；备份哈希 = 旧 exe；回滚日志与失败现场**是两个文件**；exe 恢复成旧版；服务重新在跑 |
| B 另一个真的能跑的旧构建 | 12 | 退出码 0；替换生效（哈希变）；备份 = 旧版；健康通过；**调用 2116ms 返回（< 30s 上界）** |
| C `-DryRun` | 4 | 退出码 0；exe 未替换；无备份；未启动进程 |

沙箱：`%TEMP%\yg-deploy-rehearsal`（自带 config/auths(空)/data，端口 7864），
报告落 `%TEMP%\yg-deploy-rehearsal-reports`（**沙箱外**——第一版把报告写在沙箱里，
沙箱一重建证据就没了）。**全程不读写 `D:\yg-gateway\auths` 或 `data`**（R1）。

### 2.2 排练跑出来的三个真问题（读代码读不出来）

| # | 现象 | 根因（实测） | 落点 |
|---|---|---|---|
| 1 | 健康检查报「无任何响应」，但进程明明在跑 | PS 5.1 `Invoke-WebRequest` 非 2xx 时 body 已被读进 `ErrorDetails`，`GetResponseStream()` 到此已 EOF → 空串 | `Get-HttpResult` 改直连 .NET；`/healthz` 503 的 body 照常解析 |
| 2 | 「回滚替换失败：file is being used by another process」——**回滚写不回去** | 坏 exe 的 launcher 长期占用目标文件。实测：`Copy-Item` 覆盖**失败**；写 `*.staged` + `Move-Item -Force` **成功** | `Replace-Binary`（正向替换与回滚都用它） |
| 3 | exec 被锁 15s 以上、err 日志 0 字节、launcher 永久残留 | 把非 PE 当 exe 启动 → Windows 兼容层弹**模态对话框**「不支持的 16 位应用程序」，cmd 阻塞等人点确定（**现场截图与进程证据**：cmd#25128 的 `MainWindowTitle` 就是那个对话框） | 新增 `Test-PeImage` 预检；A1 场景从「回滚」升级为「停服前拒绝」 |

第 3 条是用户在自己桌面上看到弹窗后回报的——**排练把问题引出来了，但没把弹窗这种副作用限制在沙箱里**，
这是本轮的一处真实代价，已记入 LESSONS L-12 的「规则 1」。

### 2.3 门禁

```
powershell -NoProfile -File scripts/pre-commit.ps1      # exit 0（六项全过）
```

无 `.go` 改动 → gofmt/R10 无事可做；`go build`/`go vet`/`go test ./...`（20 包 ok）/
`check-boundaries`/`check-depth` 全过。检查 3/6（PS 引号陷阱）扫过新增的 `.ps1` 行：无违规。

### 2.4 未验证的部分（明说）

- **冒烟三条没有在排练里跑过**：沙箱没账号，打真实上游必然失败。它需要一次**生产实弹**
  换版（低峰执行，会短暂重启 7863）。
- **预检只保证「是合法 PE」**：头部完好但内容损坏的 exe 仍会进入流程，由健康轮询兜底（A2 覆盖）。

## 三、怎么回滚

只新增了两个文件，**回滚 = 删掉它们**：

```powershell
git rm scripts/deploy-prod.ps1 scripts/deploy-prod.rehearsal.ps1
```

没有既有文件被修改，没有生产行为被改变，没有配置/依赖/CI 改动。
本轮**没有**在生产上执行过换版（D3 的实弹部分待用户拍板），因此生产侧无回滚事项。

## 四、提交清单

见 `git log` 中本条 change 的对应提交（一个 Task 一条提交；按显式路径 `git add`，
不用 `git add -A`）：

```
scripts/deploy-prod.ps1
scripts/deploy-prod.rehearsal.ps1
docs/prd/REQ-deploy-prod-script-2026-10-16.md
.harness/changes/2026-10-16-deploy-prod/summary.md
.harness/LESSONS.md                     （L-12/L-13/L-14）
docs/superpowers/plans/HANDOFF-2026-10-16-ci-closed.md  （若同步 N1 状态）
```

**不包含**并行会话的文件（`HANDOFF.md` / `README.md` / `.harness/rules/COST-CONTROL.md`）。
