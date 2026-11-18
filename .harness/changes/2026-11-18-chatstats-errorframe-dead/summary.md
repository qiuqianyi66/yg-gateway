# 删除 chatStatsReader 的 error 帧采集死代码

日期：2026-11-18
提交：`<待填>`
上一提交：`93a028b`

## 一、怎么发现的

查 `internal/server` 零覆盖函数清单时，`SawErrorFrame`（`logging.go:248`）在列。
零覆盖的通常解释是「缺测试」，但先做了穷尽 grep：

```powershell
# 全仓
rg 'SawErrorFrame'           → 2 命中，都在 logging.go（注释 + getter 本体）
rg 'errorFrame'              → 4 命中，都在 logging.go（字段 + 两处赋值 + getter）
```

**零调用点**，包括测试。这不是「缺测试」，是**采集了但没人消费**。

## 二、为什么这不影响正确性

`/v1/chat/completions` 流式路径早已有**另一套**error 帧采集：

```go
// chatrun_serve.go:48 —— upstream 层观察者
streamOpts = append(streamOpts, upstream.WithErrorFrameObserver(func(payload string) { errFrame = payload }))
...
case errFrame != "" {                       // :84
    kind := upstream.FrameKind(errFrame)
    h.applyErrorPolicy(acct.UID, kind, errFrame, bareModel, nil)
    st.status = http.StatusServiceUnavailable
    st.outcome = reqlog.OutcomeStreamError
}
```

那套是**活的**：分类（`FrameKind`）、账号处置（`applyErrorPolicy`）、观测收敛到 503。
`chatStatsReader.errorFrame` 是同一信息在 **reader 层**的第二份拷贝，且没人读。

所以它不是缺陷，是**冗余**。按项目「禁止堆砌」的偏好，删。

## 三、删了什么

| 位置 | 内容 | 理由 |
|---|---|---|
| `logging.go:126` | 字段 `errorFrame bool` | 无消费者 |
| `logging.go:190-198` | 两处 `s.errorFrame = true` 与 `Error json.RawMessage` 结构字段 | 同上；`Error` 只为这两处赋值存在 |
| `logging.go:247-248` | `SawErrorFrame()` getter | 零调用 |

**顺带去掉一处热路径冗余**：原代码在「解析失败或 usage 缺失」分支里**重复 Unmarshal 一次**
纯粹为了检查 `error` 字段：

```go
if json.Unmarshal([]byte(payload), &chunk) != nil || chunk.Usage == nil {
    if json.Unmarshal([]byte(payload), &chunk) == nil && len(chunk.Error) > 0 {  // ← 第二次解析
        s.errorFrame = true
    }
    return
}
```

`parseSSELine` 对**每个 SSE 帧**都跑一次（一帧一行），每帧两次 JSON 解析纯属浪费。
删除后每帧最多一次。

## 四、验证

| 项 | 结果 |
|---|---|
| `go vet ./internal/server/` | 无输出 |
| `go test ./internal/server/ -count=1` | `ok`（10.3s） |
| `parseSSELine` 覆盖率 | **100.0%**（原分支本来就没被覆盖，删掉正好） |
| 包覆盖率 | 81.2%（不变） |
| `git diff --stat` | 1 file, **+2 −13** |

删的是纯死代码 + 冗余分支，没有行为变化：`errFrame` 那条活路径不受影响（它在
`chatrun_serve.go`，与 `chatStatsReader` 无关）。

**未单独跑全量门禁**——纯删除且 `go vet` 干净，但提交前仍走 `pre-commit.ps1`。

## 五、为什么值得单独一批

1. **防误判**：这个字段的存在会让人以为「error 帧已经被采集了、有处置」，从而
   不去查 `chatrun_serve.go` 到底怎么判的。删掉后信号只有一个来源。
2. **省热路径开销**：每帧一次多余 JSON 解析。
3. **零覆盖函数清单从 13 项降到 12 项**，棘轮不会因为死代码被撑大分母。

## 六、能力边界

- 没有改 `chatrun_serve.go` 的错误处置逻辑（那条路径已有测试与实测注释，不在本批范围）。
- 剩余零覆盖函数（`KeyRPMStats`、`hasImagePart`、`rewriteModel`、`noteWaf`/`active`
  等）本批未处理，逐项仍需判断「缺测试」还是「死代码」——**不许直接批量补测试**，
  死代码补测试等于给垃圾做保鲜。

## 七、回滚

```powershell
git revert --no-edit <本批 sha>
```
