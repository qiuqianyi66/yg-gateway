# 评审记录 — 阶段 0 必要性入规范 + 前端体量入棘轮 + 沙箱副本机器核对

- 日期：2026-10-17
- 轮次：第 1 轮（自评审，六维 + 实证）
- 对象：`.harness/changes/2026-10-17-necessity-stage-and-parity/summary.md` 所列 7 个文件

## 结论

**PASS**（无 🔴 / 🟡；🟢 3 条、🔵 2 条）

## 一、六维结论

| 维度 | 结论 | 依据 |
|---|---|---|
| 正确性 | 通过 | ①`check-req-necessity.ps1` 三种输入路径都实测过：本地（含未跟踪的探针 → 红）、CI 形态（`-Base` 走 `--diff-filter=A`）、空输入（→ 绿并提示跳过）；②沙箱比对用 `runtime.Caller` 而不是 `os.Getwd`（第一版用相对路径直接红——沙箱已 chdir，这是隔离生效的证据）；③PowerShell 里 `Get-Content`/`git show :path` 两条读取分支均可用 |
| 门禁退化风险 | 通过 | 新检查是**加法**：脚本自身 `exit 2` 时 pre-commit 同样当失败（fail-closed）；`-Base` 透传保证 CI 与本地同判据；`maxFrontendLines` 缺字段时 `[int]$null = 0` → 任何 JS 都判回退（收紧，不是放水） |
| 红线 | 通过 | R10：脚本里没有 `-ErrorAction SilentlyContinue`、没有 `_ =` 形态；只读文件与 git 元数据，不写仓库根、不碰 `auths/`。L-15：必要性文档里每条"不做"都给了可复现命令（`git grep` 穷尽 `.go`、`go test -count=3` 实跑、`Get-PSDrive` 实查、`Compare-Object` 实测行数） |
| 一致性 | 通过 | 规范的判据（`^#{2,6}\s*.*必要性`）↔ 脚本实现 ↔ REQ 的 F-1001 三处措辞一致；十阶段表新增的阶段 0 与「使用方式」的 🔴 条目互相指向 |
| 可维护性 | 通过 | 新指标注释写清"为什么不塞进 `maxFileLines`"（那是放宽 `.go` 棘轮）；沙箱测试顶部列出四条白名单差异及"改它们要同步改本测试" |
| 架构合规 | 通过 | 无新包、无新依赖、无依赖方向变化（`check-boundaries.ps1` 实测通过）；新测试在原包内 |

## 二、问题清单

| 级别 | 位置 | 问题 | 处置 |
|---|---|---|---|
| 🟢 | `internal/pool/sandbox_parity_test.go` | 归一化时**丢掉全部注释行**，所以"两份文件的注释分叉"不会被发现 | 刻意：注释里本来就有互指说明（措辞必须不同），且行为由代码行决定。记录即可 |
| 🟢 | `scripts/check-depth.ps1` 度量 5 | 只扫 `internal/**/*.js`，`index.html`/CSS 不在内 | 当前二者远小于 app.js；重评条件写进 summary §六.3 |
| 🟢 | `scripts/check-req-necessity.ps1` | 只校验"有没有这一段"，不管内容 | 这是 D-1 的刻意选择：判断质量靠评审，不靠正则 |
| 🔵 | `scripts/check-req-necessity.ps1` | `git rev-parse --verify --quiet "$Base" ` 里多一个空格 | 无行为影响（PS 忽略） |
| 🔵 | `scripts/pre-commit.ps1` | 检查 8/8 排在最后，`-Fast` 时前面已跑完昂贵的边界/深度 | 位置合理：静态合规检查放最后不影响语义，失败仍进 `$failures` |

## 三、实证命令（本机实跑）

```
$ check-req-necessity.ps1                     → exit=0  [OK] 均含「必要性」段（1 个）
$ 造 REQ-teeth-probe（缺段）→ 同上            → exit=1  [FAIL] 缺「必要性」段：docs/prd/…
$ 删探针后复跑                                 → exit=0
$ app.js 加一行 → check-depth.ps1             → exit=1  REGRESSED: maxFrontendLines 4378 → 4379
$ 往 pool/main_test.go 加一行 → go test -run TestSandboxFilesInSync
                                              → FAIL  两份沙箱文件行数不一致（归一化后 253 vs 252）
$ pre-commit.ps1（全量 8 项）                  → exit=0  66.6s
```

## 四、未覆盖（未覆盖 = 未知）

1. **CI 上首次真实运行**：`gate` job 里 `-Base origin/main` 的实际行为未在 GitHub Actions 上验证过
   （本机模拟了同参数，但没有 runner 环境）。首个 push 的 CI 结果即验证。
2. **浏览器级 E2E** 仍缺（与本变更无关，影响 D-5 的重评条件）。
