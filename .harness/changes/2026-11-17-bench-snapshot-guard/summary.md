# cmd/bench 补最小守护（M1 输入正确性）

日期：2026-11-17
类型：测试补强（不改运行时行为）
轨道：E 工程化与 Harness / C 测试质量

## 一、缺陷（改前证据）

`cmd/bench` 覆盖率 **0.0%**，而它产出的三项指标（M1 选号到期偏置 / M2 池健康度 /
M3 路由成功率）是**决策输入**。改坏 `readSnapshots` 不会有人发现。

`readSnapshots` 决定 M1 用的是**哪一天**的账号现态：

```go
sort.Strings(names) // YYYY-MM-DD.json 字典序 = 时间序
latest := filepath.Join(dir, names[len(names)-1])
```

取错一份 → M1 拿旧数据冒充当天，而报告里**没有任何东西**会提示这个错误。

## 二、改了什么

新增 `cmd/bench/main_test.go`，4 个用例：

| 用例 | 断言 |
|---|---|
| `TestReadSnapshotsEmptyInputs` | 空参数 / 目录不存在 / 空目录 三种输入都返回 nil 且不 panic（M1 样本 0 是合法状态，不是崩溃点） |
| `TestReadSnapshotsPicksLatest` | 15/16/17 三个文件并存时取 17 号 |
| `TestReadSnapshotsIgnoresNonJSON` | `2026-11-19.json.tmp`、`2026-11-20.txt` 不参与"最新"竞争（写入方先落 `.tmp` 再改名，半截文件被当快照会解析失败或冒充最新） |
| `TestReadSnapshotsEmptyAccountsIsNil` | 文件存在但 `accounts: []` 时返回 nil（空数组与"没有现态"在 M1 上等价；返回非 nil 空切片会让两种含义无法区分） |

**未改任何生产代码**。这是纯补守护。

## 三、验证

| 判据 | 命令 | 结果 |
|---|---|---|
| 4 用例 | `go test ./cmd/bench/ -v` | **4 PASS**（含 3 个 subtest） |
| 覆盖率 | `go test ./cmd/bench/ -cover` | **0.0% → 47.9%** |
| 格式 | `gofmt -l ./cmd ./internal` | 无输出 |
| 牙齿（变异） | 把 `names[len(names)-1]` 临时改成 `names[0]` | **FAIL**：`应取文件名最大的 2026-11-17.json（uid-new），实际 "uid-old"` |

变异时有个有用的观察：改成 `names[0]` 后**只有 `TestReadSnapshotsPicksLatest` 变红**，
`TestReadSnapshotsIgnoresNonJSON` 仍绿（因为 17 号是唯一的 `.json`，取 `names[0]` 也是它）。
两个用例各管各的行为，没有互相掩护。

## 四、能力边界

- **`main()` 仍是 0% 覆盖**：它是 CLI 入口（`flag.Parse` + `log.Fatal` + 文件写入），
  测它需要重定向全局 flag 与 os.Stdout，性价比低。`readSnapshots`（唯一的纯逻辑）
  已覆盖到 47.9%。
- **另 3 个 `cmd/` 包仍 0.0%**：`harnessrun`/`signin`/`trial` 是一次性批量运维脚本，
  外部副作用为主，单测只能测参数解析 —— 按质量地图的拒绝项**明确不补**。
- 47.9% 里未覆盖的主要是 `os.ReadDir` 失败后的 log 分支与 JSON 解析失败分支
  （都需要制造 IO 错误，成本高于收益）。

## 五、回滚

```powershell
git rm --cached cmd/bench/main_test.go
```
（删文件即可，未改生产代码。）
