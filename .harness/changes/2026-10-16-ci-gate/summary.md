# 变更总结 — 门禁接入 CI（T2）

> 承接 `docs/superpowers/plans/HANDOFF-2026-10-16-depth-next.md` §3 的 T2。
> 约束：零新功能、零新依赖、显式路径提交。

## 一、改了什么

Task 6 把规范变成了脚本，但**脚本只在本机跑**——`--no-verify`、忘记跑、或别的会话直接 push
都能绕过，而 PR 的 `paths` 过滤器还不包含 `scripts/**`，等于「改门禁不需要门禁验」。
本次把门禁接进 CI，并补上让它在 CI 里**非空转**所必需的三处修正。

| # | 文件 | 改动 |
|---|---|---|
| 1 | `.github/workflows/go-binaries.yml` | 新增 `gate` job（ubuntu-latest + pwsh）：本地每次跑的总闸，改为「全仓 gofmt + R10(diff vs base) + 边界 + 深度棘轮」；`build` 改为 `needs: [test, gate]`；PR `paths` 补 `scripts/**`、`.harness/**` |
| 2 | `scripts/pre-commit.ps1` | 新增 `-AllFiles`（gofmt 查全仓，而非「本次改动」）与 `-Base <ref>`（R10 与改动清单按 `git diff <ref>...HEAD` 判定）；子脚本调用改为主机探测（`pwsh`/`powershell`）；gofmt 定位修正（`$goExe` 退化为 `go` 时 `Join-Path` 会抛绑定错误） |
| 3 | `scripts/check-depth.ps1` | 基线路径由 `'.harness\depth-baseline.json'` 改为分段 `Join-Path`；文件行数由 `Measure-Object -Line` 改为**显式过滤空白行**；相对路径剥前缀改成跨平台写法 |
| 4 | `scripts/check-boundaries.ps1` | `.git\golist.tmpl` 改分段 `Join-Path`（Linux 上反斜杠会盖出一个怪文件） |
| 5 | `.harness/rules/开发流程规范.md` §五 | 新增「本地与 CI 的分工」表：本地=提前发现、CI `gate`=最终闸门、`-race` 由 CI 独占；写明棘轮权威值随基线文件提交、以及 CI 里「本次改动」必须用 `-Base` 算 |

### 为什么必须连带修 3 与 4（不修的话 CI 门禁是假的）

- **基线路径**：Linux 上反斜杠不是分隔符，`Join-Path $root '.harness\x.json'` 会拼出一个
  不存在的路径 → 脚本走「无基线 → 初始化并 `exit 0`」分支 → **CI 里的深度棘轮永远是绿的**。
  这比没有门禁更糟：它给出一份「已验证」的假证据。
- **`-Base`**：CI 的工作区是干净的，改动早已提交，`git diff`（工作区口径）恒为空 →
  「本次改动的文件」为空、R10 变成「检查了 0 个文件」→ **门禁空转**。
- **`-AllFiles`**：同源问题在 gofmt 上——只查「改动文件」在 CI 里恒为空。

## 二、怎么验证（硬证据，不是自述）

四件套 + 门禁基线（2026-10-16，全绿）：

```
go build ./...            exit 0
go vet ./...              exit 0
gofmt -l ./cmd ./internal （无输出）
go test ./... -count=1    23 包：20 ok + 3 no test files
check-boundaries.ps1      exit 0
check-depth.ps1           exit 0（2185 / 5 / 7 / 71.9%，与改动前逐项相同）
```

**度量口径改变的等价性**：文件行数从 `Measure-Object -Line` 换成显式过滤空白行后，
四项指标（2185 / 5 / 7 / 71.9%）与基线**逐项一致** → 证明是等价替换，不是口径漂移。

**两个负向测试（证明 CI 那份有牙齿）**：

| 探针 | 做法 | 期望 | 实测 |
|---|---|---|---|
| gofmt 漂移 | 在工作区放一个 gofmt 不干净的 `internal/logfmt/zz_gate_probe.go`，跑 `-Fast -AllFiles -Base d382968` | 退出 1 且点名该文件 | ✅ `[FAIL] gofmt 漂移: internal\logfmt\zz_gate_probe.go`，exit 1 |
| R10（差分路径） | 临时分支提交一个含 `_ = v` 的生产文件，跑 `-Fast -AllFiles -Base main` | 退出 1 且点名 file:line | ✅ `internal/logfmt/zz_r10_probe.go:7  _ = v`，exit 1；且「1 个改动文件」证明 base 差分确实枚举到了已提交的改动（不是空转） |

两个探针均已删除；临时分支已删除；工作区未留下任何探针文件（`git status` 已核对）。

**YAML 结构**：用 js-yaml 解析 `.github/workflows/go-binaries.yml` 通过——jobs =
`test, gate, race, build, publish`；`on.pull_request.paths` 含 `scripts/**` 与 `.harness/**`；
`build.needs = ["test","gate"]`；gate 步骤 = checkout(fetch-depth 0) → setup-go → 解析 base → 总闸。

## 三、怎么回滚

| 层 | 回滚 |
|---|---|
| CI 门禁 | `git revert <sha>`：`gate` job、`paths` 两行、`build.needs` 一并恢复 |
| 脚本新参数 | `-AllFiles` / `-Base` 是纯增量开关；本地不传即恢复原行为（本次已用默认形态跑过一次全量绿） |
| 脚本跨平台修正 | `git revert <sha>` 会退回反斜杠字面量——**只应在放弃 CI 门禁时回滚**，否则棘轮重新变成假绿 |
| 文档 | 纯文档 |

## 四、遗留（已知，未做）

1. **`gate` 里没有 `node --check` 与 `govulncheck`**：前者在 `test` job（前端语法），后者在
   `test` job（依赖漏洞），分工已写进 `开发流程规范.md`，不重复跑。
2. **push 事件用 `HEAD~1` 作为 base**：单提交 push 精确；多提交 push 只覆盖最后一个提交的
   R10 新增行（PR 路径不受影响）。极端情况（初始提交 / force push 到无父提交）会 `exit 2` 报错，
   不静默放过。
3. **`gate` job 内部仍会跑一次带覆盖率的全量测试**（`check-depth` 需要覆盖率），因此与 `test` job
   有约一次测试的重复耗时（Linux 上约 2 分钟）。这是「棘轮必须基于实测覆盖率」的代价，接受。
4. **跨平台行为只能在 Windows 上实测**：本机无 Linux。已消除的依赖是「反斜杠路径」与
   `Measure-Object -Line` 两处；`Get-Content`/`Get-ChildItem`/`go list` 为通用 cmdlet，
   **首次 CI 运行需确认 gate job 为绿**（待确认项）。
