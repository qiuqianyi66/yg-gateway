# 判定记录 — B1b「面板 unknown 有值态观感」：**不做真机调用；改用零成本通路+像素验证**

- 日期：2026-10-18（判定）→ 2026-10-19（像素观感补完，见 §5.4）
- 分级：**L0**（判定 + 操作步骤 + 一处测试补齐，不改产品代码）
- 关联：PLAN B1b / A3 的剩余部分

## 一、为什么不在这一轮做

面板 unknown 的「有值态」要求**真的有一条未消解的 unknown** 在 `/metrics` 上。unknown 的登记
条件不是静态配置，而是运行时事件（成本未知的模型调用等），所以：

- 在**生产**上造一条 unknown = 真实调用一个成本未知的模型（真花钱、且会污染生产观测）；
- 在**沙箱**里造 = 需要能触发该事件的账号与上游，沙箱目前只有假上游，触发不到。

两条都要么有成本、要么不成立。**这属于「需要你点头才做」的动作**（一次真实调用）。

## 二、已覆盖的部分（不是空白）

`d09dd9f` 已把徽章/提示行抽成纯函数 `unkBadge()` / `unkNoteText()` 并加 JS 级测试
`TestAppJSUnknownBadgeAndNote`（3/3 反向变异变红）。即：**渲染逻辑本身有测试**，
缺的只是「真机上一次肉眼确认」。

## 三、给你的一键复现步骤（随时可跑）

```powershell
# 1) 看当前有没有未消解的 unknown（>0 才有「有值态」可看）
(Invoke-WebRequest http://127.0.0.1:7863/metrics -UseBasicParsing).Content -split "`n" |
  Select-String 'unknowns_open'

# 2) 若为 0：调一个「成本未知」的模型触发登记（会产生真实消费，故未代跑）
#    模型名以 /v1/models 里未被实测过的为准
# 3) 再看 unknowns_open 与面板：
start http://127.0.0.1:7863/panel
#    期望：徽章显示具体条数（有值态），而不是空态文案
```

## 四、若你同意代跑

回一句「可以调一次 <模型名>」我就执行 2)，并把 `unknowns_open` 前后值、面板徽章文本、
以及消解后的状态一起记录进本文件。
---

## 五、A-7 执行结果（2026-10-18 21:5x）：不调生产，改「通路 + 渲染」双层验证

用户裁定：**不做**生产上的真机调用（不花真钱），所以本轮把「有值态」拆成两层分别验证，
上游调用 0 次、生产未触碰（PID 40588 全程未动）。

### 5.1 新发现（这条待办真正缺的东西）

前端 `unkBadge()` / `unkNoteText()` 早有 JS 级测试，但**后端字段没有任何测试钉住**：
`/panel/api/overview` 的 `accounts[].unknowns` 来自 `pool.AccountState.Unknowns`
（`internal/pool/entry.go:141`，`json:"unknowns,omitempty"`）。一旦它被改成 `nil` 或忘填，
面板徽章永远停在空态，而**「空态」与「没有未知」在屏幕上完全一样**——正是 AGENTS.md
「没覆盖 = 未知，不是没有」要防的静默失真。

### 5.2 补的牙齿

| 项 | 内容 |
|---|---|
| 新增测试 | `internal/panel/overview_unknowns_test.go`：注入 registry + `TrackCostUnknown` → overview 的 `accounts[0].unknowns` 必须带 `question`/`requires`/`revision`；**消解后必须消失** |
| 变异 | `internal/pool/state.go` 的 `Unknowns: p.openCostUnknownsLocked(...)` → `nil` → **红**：`accounts[0].unknowns=0 want 1` |

### 5.3 渲染实跑（用真实 app.js，不是重写一份逻辑）

`node` 直接 eval `app.js` 里的 `esc` / `unkBadge` / `unkNoteText`，喂一份**生产 overview 快照**
（`/panel/api/overview` 实测 6072 字节，4 个账号）并给第 1 个账号注入 2 条 unknown：

```
<span class="tag warn" title="账号 A 在模型 glm-5.2 上的实际成本未知（仅目录声明）
需要：一次上游返回 credit 的真实调用

账号 A 在模型 hy4-preview 上的成本分层未知（样本 2 条）
需要：再 N 条带 credit 的样本把推断升级为观测" style="cursor:help">2 未知成本</span>

2 个 (账号,模型) 成本未知，悬停账号卡片查看明细

空态对照：badge "" / note ""（不占位、不显示「0 未知」——四态纪律）
```

脚本与数据留在 `%TEMP%\b1b-unknown\`（`render_unknowns.js`、`overview-live.json`、`overview-stub.json`、`stub_panel.py`）。

### 5.4 像素级观感（2026-10-19 补完，零上游调用）

工具链：本会话 Playwright 未安装，改用 **chromium headless + 原生 CDP**（Node 24 自带 `WebSocket`，
脚本 `%TEMP%\b1b-unknown\cdp_shot.js`）。被测页面是**真实产线资产**：`internal/panel/index.html` +
`internal/panel/app.js`（stub 只替换 `/panel/api/overview` 的 JSON，数据取自生产 overview 快照）。

| 态 | 证据 | 实测 |
|---|---|---|
| 有值态（第 1 个账号注入 2 条 unknown） | `panel-unknowns-badge.png` | 提示行「2 个 (账号,模型) 成本未知，悬停账号卡片查看明细」；第 1 张卡右上 **「2 未知成本」** 琥珀色 warn 徽章，位置在「可用」标签**左侧**，卡片高度/网格未被撑破；徽章 `title` 为两条 question + 「需要：…」 |
| 空态（去掉 `unknowns` 字段） | `panel-unknowns-empty.png` | 无提示行、无徽章，**不显示「0 未知」**（四态纪律：缺失不是「没有」，但也不该伪造一个 0） |

CDP 直接读到的 DOM 事实（不是靠截图猜）：

```
badges = [{"text":"2 未知成本","title":"账号 A 在模型 glm-5.2 上的实际成本未知（仅目录声明）\n需要：一次上游返回 credit 的真实调用\n\n账号 A 在模型 hy4-preview 上的成本分层未知（样本 2 条）\n需要：再 N 条带 credit 的样本把推断升级为观测"}]
noteText = "2 个 (账号,模型) 成本未知，悬停账号卡片查看明细"
空态: badges 只剩「已暂停」状态标签，noteText = ""
```

**一条被排除的假警报**（顺手确认，不是产品问题）：stub 最初把 index.html 直接挂在 `/panel`（无尾斜杠），
页面相对路径 `app.js` 解析成 `/app.js` → 404 → 面板空白。生产不会这样：
`GET /panel` 返回 **307 → `/panel/`**（`internal/panel/panel.go:165` 的 `GET /panel/{$}`），
`GET /panel/app.js` 200，`GET /app.js` 404——三条实测都对。

**过程记录（两条新坑）**：①`chromium --headless --screenshot` 在本机沙箱下会因 crashpad 走命名管道失败
（`TransactNamedPipe: 管道已结束`），必须 `Start-Process` 不捕获 stdout，或干脆走 CDP；
②`--virtual-time-budget` 会在真实网络返回前结束，用它会拍到「未加载」的空壳（第一次截图全是「—」就是它）。
