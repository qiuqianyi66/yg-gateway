# 2026-11-19 面板视觉第三批：颜色深度 / 磨砂顶栏 / 悬停高光（并修掉空池永久骨架）

## 起因

用户反馈「颜色太单调，不美观，直接学大网站，渐进式加载各种特效」。要求先看 GitHub
上「AI 做前端 / 设计工程」的仓库再改，不自己拍配色。

## 参考来源（实际检索到的，不是想象）

| 来源 | 取用了什么 |
|---|---|
| `leonxlnx/taste-skill-classics`（SKILL.md 原文被检索到） | anti-slop 落地清单：内描边高光（1px inner highlight）、spotlight 悬停卡、shimmer 微动效、弹簧曲线；同时它对 app screen 明确反对渐变堆砌 |
| Flux Dashboard（DashboardPack，Next.js 模板） | 渐变主色 + 分层彩影、磨砂顶栏（backdrop-blur）、点阵背景、大圆角 |
| TDesign | 设计令牌分层的既有做法（本仓 `:root`/dark 两套已具备，未重复造） |

**没有**采纳的：Inspira UI 类 3D 粒子/极光背景（运维面板不需要，性能与可读性双输）、
渐变文字、霓虹外发光、逐卡入场动画（见下「被否掉的方案」）。

## 改了什么（全部 `internal/panel/app.css`，96 行；`app.js` 净 +-1 行）

1. **补数据色令牌**：`--violet` / `--blue` / `--violet-soft` / `--blue-soft`，亮暗两套
   都写（不写暗色会撞 `TestDarkThemeTokensComplete`）。语义色 ok/warn/bad 不动。
2. **环境层**：`body` 三层径向 mesh（绿/蓝/紫，0.15/0.13/0.11）+ 26px 点阵网格，
   `background-attachment: fixed`。此前是 `#f0f4f3` 纯色，白卡贴白底。
3. **磨砂顶栏**：`.topbar` 半透明表面 + `backdrop-filter: blur(14px) saturate(1.5)` +
   底部内高光。
4. **侧栏**：纵向 `surface → surface-2` 微渐变；选中项改「绿→蓝」渐变药丸 + 内高光
   （原来只有一块 `--accent-soft` 平色 + 左侧绿条）。
5. **主指标卡**：可用账号用「绿→蓝」双色底，积分卡用「紫→蓝」；`.stats` 容器加内高光。
6. **KPI 卡**：把左侧色条的颜色以 10% 渗进卡面（`color-mix`），四张卡各染各的；
   `.k`/`.s` 提 `--ink-2`。
7. **分区头**：`.box > header` 加 `surface-2 → surface` 纵向过渡，白卡不再「一条硬线切头」。
8. **悬停高光**：`.acc-card` / `.stat.hero` 的 `::before` 静态径向光斑，悬停淡入；
   卡片悬停上浮 2px + 彩色阴影；`.acc-avatar` 改绿→蓝渐变。
9. **视图切换淡入**：`.view:not([hidden])` 一次性 4px 上浮 + 淡入。
10. 全部动效 `transform/opacity`，并被既有 `prefers-reduced-motion` 全局规则关闭。

## 顺手修掉的真 bug（截图暴露，不是样式）

**空账号池永远停在骨架屏**：`accRenderSig` 初值是 `''`，而 `accountsSig([])` 的返回值
也是空串 → 首屏骨架（boot 时直接写进 `accBody`、不走签名）被「数据没变」短路挡住，
成功渲染永远不执行。上一批只修了「401 错误条残留」，这次在暗色截图里才看到骨架版。
修法：初值改 `null`（`null` 不等于任何真实签名），注释写明为什么不能用空串。

## 被否掉的方案（都试过，附拒绝理由）

- **逐卡入场动画（`--i` 错峰）**：`renderAccounts` 签名含 `credits`/`in_flight`，每 5s
  轮询都会变 → 卡片整片反复重播动画，变成闪烁。撤销。
- **指针跟随光斑（--mx/--my）**：需要 `pointermove + rAF + getBoundingClientRect`。
  放 `app.js` 会把文件从 1368 顶到 1393，`check-depth` 的 `maxFrontendLines` 棘轮
  立刻红（只许收紧，禁止 `-Update`）；新起 `app_effects.js` 又要同时改 embed 变量、
  路由、脚本清单、顺序测试四处 Go 代码。收益只是光斑位移，成本不成比例 → 改纯 CSS
  静态高光。
- **隐藏空数据卡片**：属产品判断（要不要让用户知道模块存在），未擅自做。

## 怎么验证

| 验证 | 结果 |
|---|---|
| `go test ./internal/panel/...` | ok（57s；中途曾因 `TestAppJSBootOrderGuards` 红，已修：`start();` 必须仍是最后一行） |
| `scripts/check-depth.ps1` | 通过（app.js 回到 1368、app.css 966、index.html 997，均 < 1000） |
| `scripts/pre-commit.ps1` | 11/11 全过，exit 0 |
| e2e 面板冒烟 `npm test` | 4 passed / 32 断言，无 JS 报错 |
| 亮色截图 | `%TEMP%\panel-v3\*.png`（10 视图）：双色主指标卡、磨砂顶栏、渐变侧栏、染色 KPI |
| 暗色截图 | `%TEMP%\panel-v3\accounts-dark.png`：令牌完整、渐变可用、空态正常 |

## 未验证 / 残余

- 悬停高光与卡片上浮**只有静态截图**，没有录到 hover 态像素。
- `backdrop-filter` 在低端机/远程桌面下可能被禁用，此时退化为半透明底（不报错）。
- 深色主题只拍了账号池一屏，其余视图未逐屏核对。
- 生产实例（有真实账号）的观感需部署后另拍，本批截图全部来自 0 账号隔离实例。

## 回滚

`git checkout -- internal/panel/app.css internal/panel/app.js` 并重新构建替换。
纯样式 + 一处初值，无数据迁移；回滚后空池会回到「骨架永不替换」的旧缺陷。
