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
| `scripts/deploy-prod.ps1` | 745 | 生产换版一条命令：备份（R15）→ 等端口释放 → PE 预检/替换 → 启动（cmd 重定向落 stderr）→ 健康轮询 → 冒烟 → 失败自动回滚；每步落 JSON 报告 |
| `scripts/deploy-prod.rehearsal.ps1` | 418 | 上述脚本的排练：在 `%TEMP%` 沙箱（自己一份 config/auths(空)/data、端口 7864）跑 **5 个场景**，41 项断言，**不碰生产** |

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
| 1 | 本次换版未生效且**已确认旧版在跑**：前置检查失败（生产未被触碰）/ 停服或替换失败但**已把旧版重新拉起**。旧版拉不起来就是 4，不是 1（见 §2.8 C1） |
| 2 | 新实例健康检查失败 → **已自动回滚**，服务回到旧版 |
| 3 | 部署+健康都过，但**冒烟失败**（不回滚） |
| 4 | 任何一步失败且**旧版没能回到运行状态**，或未预期错误（**生产可能不可用**，看 stderr 日志） |

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

### 2.4 生产实弹（2026-10-16 21:15–21:18，用户批准后执行）

| 轮次 | 结果 |
|---|---|
| 第 1 轮（**我的调度串写坏了**，见 §2.6） | 换版本身成功：生产 `4EE83B41` → `8F357C25`，备份 `yg-gateway.exe.pre-n1-livefire-20261016.bak` = 旧 T7（哈希核对一致），健康 3/4。**但报告/控制台日志路径被坏串切成垃圾，冒烟结果丢失** |
| 第 2 轮 | 全流程可见，`stage=smoke-failed`：`/v1/models` 200（20 个模型）、流式过、**非流式判失败（HTTP 200）**。脚本行为正确——**冒烟失败不回滚**、备份保留、报告落盘 |
| 第 3 轮（修掉判据后） | ✅ **`stage=done`、`health_ok=true`（healthy 3/4）、`smoke_ok=true`、`rolled_back=false`、用时 5.5s**；冒烟用模型 `cn:deepseek-v4.1-flash` |

**生产终态**：exe = `8F357C25…`（当前 HEAD 的构建），健康
`{"healthy":3,"realm_servable":{"cn":true,"global":false},"total":4}`，
监听进程 = `data\yg-gateway.pid` 里的 26988（**pid 文件写入这条也顺带实证了**：第 2/3 轮都是
「按 pid 文件停」）。备份共三份：T7 一份 + 新构建两份（同哈希，留作审计）。

> 退出码注：包装 `.cmd` 的 `echo EXITCODE=%ERRORLEVEL%` 没展开（最后一轮），所以退出码没直接
> 落到日志。但脚本只在 `$exitCode -eq 0` 时打印「换版完成」，第 3 轮打印了它，且报告
> `stage=done` —— 退出码 0 由脚本自身的分支条件确定。

### 2.5 实弹暴露的两个问题（都是**判据**的问题，不是网关的问题）

**① 非流式冒烟判据太天真（已修，第 3 轮验证）**

第 2 轮的现象是「HTTP 200 却判失败」。手工复现拿到原始响应：

```json
{"choices":[{"finish_reason":"length","index":0,
  "message":{"content":"","reasoning_content":"We need","role":"assistant"}}],
 "model":"hy4-preview", ... "completion_thinking_tokens":16, "completion_tokens":16, ...}
```

**根因**：目录里第一个模型是 `cn:auto` → 路由到**推理模型** `hy4-preview`，`max_tokens: 16`
全被 `reasoning_content` 吃掉 → `content` 为空、`finish_reason=length`。这是**模型行为**，
网关转发完全正常（合法 chat.completion 信封）。

- **旧判据**：`content` 非空 → 在推理模型上必然假红。
- **新判据**：200 + ≥1 个 choice + 该 choice 的 `message` 里 `content` 或 `reasoning_content`
  有内容 → 表达的是「网关转发成功并回了一个合法响应」这个真正的契约。
  仍有牙齿：`choices` 为空、`message` 缺失、两字段皆空、非 200，逐条会红。
- 顺带把**用了哪个模型**记进报告（`smoke_model`）——这次为了定位它多花了一个来回。

**② 流式判据有个洞（已修）**

旧判据是「含 `data:` **且** 含 `[DONE]`」，而 `data: [DONE]` **一行同时满足两者**——
「只回了终止符」的退化响应会被判通过。新判据额外要求**至少一行非 `[DONE]` 的 `data:` 行**
（真正的增量帧），并把 `data 增量帧=` / `[DONE]=` 两个布尔写进详情。

### 2.6 我的调度串写错了（如实记录，非脚本缺陷）

第 1 轮我用「字符串拼接」生成包装 `.cmd`：

```powershell
'... -ReportPath "' + $tmp + '\n1-livefire-report.json" ...'
```

命令被运输层按行拆开后，`.cmd` 里变成 `-ReportPath "`→换行→`C:\...\Temp`→换行→`\n1-...`，
cmd 把下一行当成新命令、重定向目标断成两截 → **报告与控制台日志都没落盘**（所以第 1 轮只有
「生产确实换了版」这一项证据）。这与 L-1/L-10 同族：**在行敏感的地方用字符串拼接造文件**。
第 2/3 轮改成用编辑工具写**字面** `.cmd`（内容里用单引号 PS 串 + `*>&1 | Out-File` 抓全部输出流），
问题消失。已作为候选补充进 L-10 的规则 1（「别拼行」的同族形态）。

### 2.7 仍未验证的部分（明说）

- **受伤路径的实弹**：本轮的坏 exe / 非 PE 两条路只在**排练沙箱**里跑过（排练 A1/A2 全绿），
  没有在生产上故意喂坏 exe —— 那会真的承担回滚风险，不值得为「多一层证据」去换。
- **预检只保证「是合法 PE」**：头部完好但内容损坏的 exe 仍会进入流程，由健康轮询兜底（A2 覆盖）。

### 2.8 独立复审驱动的修正（一条 Critical、两条 Important + 场景 D）

本轮落地后按纪律做了一次**全新上下文复审**（另一个只读 agent 复审 `09f4367..c098f65`），
它给出一条 Critical、两条 Important 与若干 Minor。逐条核对后**全部成立**，已全部修：

| # | 问题 | 修法 |
|---|---|---|
| **C1（Critical）** | **停服之后的两条失败路径（端口没释放 / 替换失败）直接 `exit 1`，而回滚块只挂在「健康检查失败」分支里** → 服务停在「旧版已杀、新版没起来」，而退出码表把 1 写成「生产未被改动或已恢复原状」。回滚是这脚本唯一存在的理由，却对这条路径不可达 | 回滚收敛为函数 `Invoke-Rollback`，**所有**失败分支都走它；`1` 的含义收紧为「已确认旧版在跑」，拉不起来一律 `4`。排练新增**场景 D** 复现并钉住 |
| I1 | `taskkill` 前不核身份：pid 文件会过期（PID 复用）、端口监听者可能是别的程序——与本文件自己声明的「不替你 taskkill 陌生进程」直接矛盾 | 新增 `Stop-OwnedProcess`：核对进程路径是否就是要换的那个二进制；读不到路径也按「不杀」处理。`Update-PidFile` 同样只在身份命中时写（防下次按错的 pid 去杀） |
| I2 | 替换后**没有**核对哈希（备份侧一直有）：若替换静默没发生，健康检查与冒烟都会由**旧版本**满足 → `exit 0` / `stage=done`，而换版没发生 | 替换后断言 `exe_hash_after == source_exe_hash`，不符即按替换失败走回滚 |
| Minor ×5 | `stage='replaced'` 在替换**之前**赋值（报告与事实相反）；回滚前丢弃 `Stop-Listener` 的返回值；`modelsCache` 拆成两次取锁（丢失改前的原子性）；`gate.yml` 里「两条发布线」的陈旧引用；本 summary 的行数与 §三 文案过时；L-3 写着「零误报」而字面名分支其实有出口 | 逐条修。凡是我本轮自己写下的**事实性错误**（不是口味问题）一并纠正——留一条「已知为假」的记录比不写更糟 |

**场景 D 的 RED→GREEN**（C1 的牙齿）：

```
RED（修前）：
  [FAIL] 报告 stage 不得写成 replaced（替换并没发生），实际 replaced
  [FAIL] 报告 rolled_back = true，实际 false
  [FAIL] **替换失败后旧版仍在跑**（端口无监听、/healthz 无响应）

GREEN（修后）：
  [OK]   退出码 = 1（替换失败，但已把旧版重新拉起 = 符合「已恢复原状」的语义）
  [OK]   报告 stage 不得写成 replaced，实际 rolled-back
  [OK]   报告 rolled_back = true / rollback_failed = false
  [OK]   **替换失败后旧版仍在跑** / /healthz 有响应
```

构造方式：以 `FileShare.Read` 独占打开目标 exe（不给 `FILE_SHARE_DELETE`）→ 改名替换必被拒；
不用 `FileShare.None` 是因为那连**启动**都会被拒，场景会退化成「什么都起不来」、验不到修复点。

## 三、怎么回滚

只新增了两个文件，**回滚 = 删掉它们**：

```powershell
git rm scripts/deploy-prod.ps1 scripts/deploy-prod.rehearsal.ps1
```

没有既有文件被修改，没有生产行为被改变，没有配置/依赖/CI 改动。
**生产已换版到 `8F357C25`（当前 HEAD 的构建）**，回滚方式：

```powershell
# 回到实弹前的 T7 版本（脚本会先把当前 exe 备份成 yg-gateway.exe.pre-<后缀>-<日期>.bak）
powershell -NoProfile -File scripts/deploy-prod.ps1 -Exe D:\yg-gateway\yg-gateway.exe.pre-n1-livefire-20261016.bak -Feature rollback-to-t7
```

`D:\yg-gateway` 里现有三份备份（`pre-n1-livefire{,-2,-3}-20261016.bak`）：第一份是 T7（`4EE83B41`），
后两份是本次新构建的副本，留作审计。**未在生产上验证过的路径**：非 PE 与坏 exe 两条只在排练里跑过
（故意在生产喂坏 exe 要真的承担回滚风险，不值得为「多一层证据」去换）。

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
