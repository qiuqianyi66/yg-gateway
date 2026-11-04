# gamify-boot-fix 变更摘要

> 目录名与部署 `-Feature` 标签同名（main 上直接落地，未开 feature 分支）。
> 本文件是这次改动的审计凭据。

触发：用户报「前端优化了但页面没变化」，随后报「好多东西都加载不出来」。
分级：L2（前端运行时缺陷 + 部署 + 回归测试）
ADR：不需要（无数据模型/上游协议/难逆转决策）

---

## 一、改了什么

| 文件 | 作用 / 改动 |
|---|---|
| `修改：internal/panel/app.js` | ① `queueTimer/lastQueueSeq` 声明从第 1141 行上提到文件顶部（与 `refTimer` 同处）。原写法用 `typeof queueTimer` 守卫 visibilitychange，但 **`typeof` 对 TDZ 中的 let 会抛 ReferenceError**，只对未声明变量安全——监听一触发必崩。② `start()` 从第 1035 行移到文件末尾：原位置早于 `US_DIMS/US_DIM_TABS/US_CREDIT_TABS/usageRateWarmAt`（1246-1255）求值，`start()` → `loadOverview` → `loadUsage` 命中 TDZ，用量页首次进入即「读取失败」且 catch 分支再次读取同一常量、二次抛错 |
| `修改：internal/panel/app_views_gamify.js` | `renderStreak` 插入 streakStrip 时用 `qprog.parentNode.insertBefore(el, qprog)`：`#qProg` 的父节点是 `.box`（孙层），`host.insertBefore(el, qProg)` 会抛 NotFoundError 并中断整条 `start()`（成就带、鉴权门一并没跑） |
| `修改：internal/panel/app_views_achievements.js` | 同型修复：`ref.parentNode.insertBefore(el, ref)`（ref 可能是孙层节点） |
| `修改：internal/panel/index.go` | `setSecurityHeaders` 增加 `Cache-Control: no-cache, must-revalidate`。资源 URL 不随内容变化（go:embed），无缓存头时浏览器按启发式缓存 app.js/css，换版后仍显示旧界面（本次实际踩到） |
| `新增：internal/panel/gamify_dom_test.go` | ① `TestGamifyStripsInsertIntoNestedContainer`：node + **有真实父子约束的 DOM 桩**（`insertBefore` 参照节点非直接子节点即抛错），跑 renderStreak/renderAchievements；既有 `TestAppJSTopLevelSmoke` 用 inert Proxy 把一切都吞掉，对参数语义错全盲。② `TestAppJSBootOrderGuards`：静态闸门——app.js 不得出现 `typeof queueTimer`、`queueTimer` 声明必须在 visibilitychange 之前、文件末尾必须是 `start();` |

**没做的（明写，免得下轮重提）**：
- 面板需要 API key 的账号池/用量数据渲染未在自动化浏览器里做真数据验收（该浏览器 profile
  无 key，只有 401；不读用户 localStorage 密钥）。已用「无 key 降级路径」验收：用量页各
  维度 Tab 正常渲染、错误块可读、可重试，不再抛异常。
- `/favicon.ico` 仍 404（浏览器控制台一条噪音），本次不改：index.html 有行数棘轮，加一行会顶额度。
- 生产部署已做（见下）；回滚靠 `deploy-prod.ps1` 自动备份 + 报告里的 `backup_path`。

## 二、怎么验证的

| 验证 | 命令 / 手段 | 结果 |
|---|---|---|
| 面板包测试 | `go test ./internal/panel/...` | ok（54.1s，两轮） |
| 新测试 | `-run "TestGamifyStripsInsertIntoNestedContainer\|TestAppJSBootOrderGuards"` | PASS |
| **牙齿验证** | 把 gamify 插入改回 `host.insertBefore(el, qprog)` 再跑 | **FAIL**（harness 抛 NotFoundError），改回后 PASS —— 测试对旧写法确实变红，不是空转 |
| 依赖方向 / 体量棘轮 | `scripts/check-boundaries.ps1`、`scripts/check-depth.ps1` | 通过（深度指标未回退；前端最大 1827 行） |
| 同源校验 | 从工作区重编 → 与 `D:\yg-gateway\yg-gateway.exe` 比对 SHA256 | 前 16 位一致 `4C8D7FE3A23C2674`（部署的 exe 就是当前源码构建） |
| 部署 | `scripts/deploy-prod.ps1 -Exe ... -Feature gamify-tdz-fix` | 退出码 0；备份/PE 预检/健康轮询/冒烟三条全过；报告 `D:\yg-gateway\data\logs\deploy-prod-20261104-110850.json` |
| 线上资源 | `Invoke-WebRequest` 逐个抓首页引用的 18 个 src/href | 全 200；`/panel/app_views_achievements.js` 从 404 → 200；`Cache-Control: no-cache` 已生效 |
| 运行时（真浏览器 console） | bu 打开 `/panel/`，逐个切 9 个视图，抓 console | 修复前 3 条异常（insertBefore / US_DIM_TABS / queueTimer）；修复后 **非 401 报错 0 条**；任务中心 DOM 内 `#achStrip`、`#streakStrip` 均存在且渲染出 5 枚成就徽章 |

## 三、怎么回滚

```powershell
# 报告里的 backup_path 指向本次换版前备份；直接换回并重启
Copy-Item "D:\yg-gateway\data\ backups\<报告里的 backup_path>" "D:\yg-gateway\yg-gateway.exe" -Force
```
或重跑 `scripts/deploy-prod.ps1 -Exe <旧备份>` 走完整冒烟。
