# 变更记录 — 两条发布线合并为一条 `release.yml`（D2 / N8）

- 日期：2026-10-16
- 来源：`docs/superpowers/plans/HANDOFF-2026-10-16-ci-closed.md` §3 N8 与 §7 D2（**用户已拍板：合并**）
- 级别：L2（CI 编排；不改生产代码、不改门禁实现、不改依赖方向）

## 一、问题

上一轮把门禁抽成了可复用 workflow `.github/workflows/gate.yml`，但 `go-binaries` 与
`docker-ghcr` **各自** `uses:` 它一次 → 每次 push 门禁跑两遍，重复约 2.5 分钟。

门禁**必须**共用同一份实现（复制成两份必然分叉，分叉后会给「这条线过了、那条线没过」的
假保证），所以消除重复的正确做法是**合并成一条 workflow**——一个 `gate` job + 两个产物 job——
**而不是**把门禁内联回去各写一份。

## 二、改了什么

| 文件 | 改动 |
|---|---|
| `.github/workflows/release.yml` | **新增**：合并后的发布流水线（`test` / `gate` / `race` / `build` / `build-image` / `publish`） |
| `.github/workflows/go-binaries.yml` | **删除**（内容并入 release.yml） |
| `.github/workflows/docker-ghcr.yml` | **删除**（内容并入 release.yml，`build-and-publish` → **`build-image`**） |
| `scripts/pre-commit.ps1` | 注释里的 CI 用法指向 release.yml（活引用） |
| `docs/adr/ADR-008-*.md` | 相关链接指向 release.yml 的 gate job，并注明合并前是两个文件（活引用） |

### 2.1 合并后仍然成立的三条保证

1. **门禁是两条产物线的共同前置**：`build` 与 `build-image` 都 `needs: gate`（原来分别是
   `needs: [test, gate]` 与 `needs: gate`）→ 门禁不过就不产出二进制、不打镜像。
2. **每个 job 的代码逐行未改**（机械核对，见 §3.1）：`build-image` 只是把
   `build-and-publish` 改名，代码行零差异。
3. **触发条件取并集**：PR 的 `paths` 合并了原两条线的清单。副作用是「只改 `Dockerfile` 的 PR」
   现在也会跑 `test`/`race`/`build`（原来只跑镜像线）——多花分钟数，换「不会有一条产物线
   悄悄没被验证」。

### 2.2 顺带修正的一处旧注释

原 `docker-ghcr.yml` 的并发注释写的是「发版时 main+tag 同推不再双跑同一份代码的多架构构建」。
合并后这个理由**更强**了（原来靠两条 workflow 的 concurrency 各自去重，现在是同一份
concurrency 组直接去重），注释与实现一致，保留原文。

## 三、怎么验证的

### 3.1 本地：机械核对（在没有 YAML 解析器的环境里，这比语法检查更有价值）

本机没有 YAML 解析器（bundled Python 无 `pyyaml`，pip 无网；node_modules 里也没有 `yaml`/`js-yaml`），
所以用**两件**代替：

**① 每个 job 的代码行与原文件逐行比对**（`Compare-Object`，忽略注释与空行）：

```
✅ test 的代码行与源逐行一致（14 行）
✅ gate 的代码行与源逐行一致（3 行）
✅ race 的代码行与源逐行一致（10 行）
✅ build 的代码行与源逐行一致（52 行）
✅ publish 的代码行与源逐行一致（40 行）
✅ build-image 的代码行与源逐行一致（41 行）
   （build-image ← build-and-publish 的代码行差异数 = 0）
```

> 第一次比对报了 3 处「差异」，全是 **job 之间的注释行**被切分器归到了上一个 job。
> 只看代码行后为零差异——这条说明「切分粒度」本身也会伪造结论。

**② YAML 杀手级检查**：含 TAB 的行 = 0；缩进非 2 的倍数的行 = 0；冒号后缺空格的行 = 0；
流式集合（`[...]`/`{...}`）不配平的行 = 0；顶层键 = `name`/`on`/`concurrency`/`permissions`/`env`/`jobs`。

### 3.2 CI：push 后看运行图（真正的验证）

见 §四。判据：**一个 workflow、一次 gate**，且 `build` 与 `build-image` 都在 gate 绿之后才起。

## 四、实测（CI）

push `9c68947` 触发 run **`38908276493`**（`push` → main）：

| 证据 | 结果 |
|---|---|
| `gh workflow list --all` | 只剩 `gate`（386638891）与 `release`（386722644）——旧两条已消失，`release` 显示 **active**（GitHub 只注册合法工作流，这本身就是 YAML 合法性验证） |
| 本次 push 触发的运行数 | **1 个**（`release`）。同一列表里上一次 push（`09f4367`）是**两个**运行（`go-binaries` 38897943909 + `docker-ghcr` 38897943777），各自带一个 gate |
| 整体结论 | completed / **success** |
| gate 执行次数 | **1 次**（`gate / gate`，13:29:51 → 13:31:16） |
| 前置关系（关键） | `test`/`race`/`gate` 均 13:29:51 起；**5 个 `build` 与 `build-image` 都在 13:31:18–13:31:23 起**，即 gate 13:31:16 变绿之后 → 「门禁不过就不产出产物」仍然成立 |
| `publish` | skipped（非 tag 触发）✅ |
| 各 job | test 13:29:51→13:31:05；race →13:31:39；build（5 平台）→13:31:46～13:32:03；build-image 13:31:19→**13:38:26** |

**耗时口径要说清**（避免把一件事说成两件）：

- 这次运行总时长约 **8.6 分钟**，比之前「两条线并行约 2 分 13 秒」的**墙钟更久**——原因是
  `build-image` 本身跑了 7 分钟：本次 push 改了 Go 源码，Dockerfile 里的 Go 构建层缓存失效，
  两个架构（amd64 + QEMU 模拟 arm64）都要真的重编。上一次 1 分 58 秒是**全缓存命中**。
  这与合并无关（合并前后都会遇到），属于「改了源码就要重编镜像」的正常代价。
- 合并**真正省下**的是：**少跑一次 gate**（每次 push 省下一个 gate job 的 runner 分钟，
  实测该 gate 约 1 分 25 秒）。这正是 D2 要解决的问题。

## 五、怎么回滚

```powershell
git revert <本变更的提交>
```

回滚后 go-binaries.yml / docker-ghcr.yml 复活（原文件在 git 历史里，逐字可取），
门禁恢复「跑两遍」的状态。生产侧无动作（CI 编排不影响运行时）。

## 六、执行中作出的裁决

- **保留历史文档里的旧文件名不改**（`docs/superpowers/plans/HANDOFF-2026-10-16-*.md`、
  `docs/superpowers/plans/2026-10-15-*.md`）：它们记录的是**当时的事实**，改它们等于篡改历史；
  只更新 ADR 与脚本注释这类**活引用**。**代价（若判断错）**：读者在旧交接文档里会看到
  已不存在的文件名——由本轮新交接文档的「取代」说明兜住。
- **不合并 `test`/`race` 进 `gate`**：`gate` 是「治理门禁」（实现与本地 pre-commit 同源），
  `test`/`race` 是「测试」；把它们塞进可复用 workflow 会让门禁的门槛随测试环境变化。
  保持 `needs: [test, gate]` 不变。**代价（若判断错）**：每次 push 多一个 job 的调度开销（秒级）。
