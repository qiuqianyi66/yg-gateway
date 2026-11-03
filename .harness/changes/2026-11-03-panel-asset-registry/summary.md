# 面板资源清单生成式（消除 index.html 结构性增长）

日期：2026-11-03

## 背景

`internal/panel/index.html` 尾部手写了 17 行 `<script src=...>`，head 里手写 `<link rel="stylesheet">`。
页面骨架正被深度棘轮 `maxFrontendLines` 约束（只许收紧），于是出现一个结构性缺陷：

> **每新增一个前端组件，就要吃掉一行 index.html 的棘轮额度。**

实测触发：`游戏化微动效层（P0）` 往 index.html 加了 `<link>` + `<script>` 两行，
`maxFrontendLines` 从 1842 顶到 1844，`check-depth` 直接 `exit 1`：

```
❌ 深度指标回退（1 项）：
   REGRESSED: maxFrontendLines 1842 → 1844
```

这不是那次改动写错，而是**清单被手写在受棘轮约束的文件里**这个设计错的。

## 改法

资源清单收敛到 Go 侧一处，`index.html` 只留两个占位符：

| 位置 | 内容 |
|---|---|
| `index.go` `panelStyles` / `panelScripts` | 加载顺序的**唯一真相**（顺序即依赖顺序） |
| `index.html` head | `<!--panel-styles-->` |
| `index.html` body 尾部 | `<!--panel-scripts-->` |
| `renderIndexHTML()` | `sync.Once` 缓存，首次请求把占位符展开成实际标签 |

`index` handler 改为写 `renderIndexHTML()` 的产物，不再写原始 `indexHTML` 字节。

**静默失效防护**（这是本次最容易踩的坑）：占位符改名而 `renderIndexHTML` 没同步时，
`strings.Replace` 会**静默不替换** → 页面没有任何脚本 → 白屏，而 Go 侧编译与测试全绿。
所以新增两条测试：

- `TestIndexHTMLHasNoHandWrittenAssetTags` —— 骨架里出现任何 `<script src>` / `<link rel=stylesheet>` 即红
  （牙齿：把任意一行标签抄回 index.html 立刻失败）
- `TestIndexHTMLPlaceholdersReplaced` —— 双向断言：占位符不得残留，且 `panelScripts`/`panelStyles`
  的每一项都必须出现在下发产物里

同时 `frontend_bundle_test.go` 的 `scriptFiles()`（全包前端测试的单一取源码入口）改为读
`renderIndexHTML()` 产物。**若仍读原始 index.html，它会拿到空清单，依赖它的二十多处断言会静默空转**——
这正是 2026-11-02 拆分时立的那个前端失效模式，此处按同一逻辑处理。

## 验证

- `pre-commit.ps1` **11/11**（曾因 R10 静默忽略拦下 `_, _ = w.Write(renderIndexHTML())`，
  已补「忽略」论证注释，与本文件其余静态资源 handler 同口径）
- `internal/panel` 全量测试通过（含新增 2 条）
- `index.html` 行数 **1844 → 1827**（非空口径，与 check-depth 一致）
- `check-depth -Update`：`maxFrontendLines 1842 → 1827`（唯一变动项，其余指标不动）

## 回滚

三处，均为纯机械：

1. `index.html`：两个占位符还原成原来的 17 行 `<script>` + 一行 `<link>`；
2. `index.go`：`index` handler 改回写 `indexHTML`，删除 `panelStyles`/`panelScripts`/
   占位符常量/`renderIndexHTML`；
3. `frontend_bundle_test.go`：`scriptFiles()` 改回 `os.ReadFile("index.html")`，删掉两条新测试。

数据/凭证/wire 形状**零影响**（只动面板静态资源的下发方式）。

## 边界（重要）

本目录只描述**面板资源清单生成式**这一件事。同一次提交里还包含
`游戏化微动效层（P0, 2026-11-03）` 的在办改动（`app_views_gamify.js` / `gamify.css` /
`gamify_test.go` / `app.js` / `app_views_accounts.js` / `panel.go` / `security_test.go`
及其 PRD 与 `.harness/changes/gamify-p0/`）。

**两者不可拆成两个提交**，原因有二：

1. `panelScripts` / `panelStyles` 引用了 gamify 的两个文件，且 `//go:embed` 也是随 gamify 引入的——
   只提交本重构会在 embed 阶段编译失败；
2. 只提交 gamify（手写标签版）则 `maxFrontendLines 1844 > 1842`，该提交自身门禁为红。

所以这次按「gamify 引入压力 + 本重构消化压力」合并为一个可独立验证绿的提交。
若后续要按作者归属拆分，需重排 `index.html` 的标签写法（不可行）或放宽棘轮（不建议）。
