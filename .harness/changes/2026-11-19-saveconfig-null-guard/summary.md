# 2026-11-19 saveConfig 拒绝非对象提交

## 改了什么

`cmd/server/main.go` `saveConfig`：`json.Unmarshal(raw, &incoming)` 之后加一道
`incoming == nil` 守卫，返回 `提交的配置不是 JSON 对象`。

理由（实测链路，不是推测）：`null` 解到 `map[string]any` 是零值 nil map 且
`err == nil`；不挡住就会走完 `mergeConfigMaps(cur, nil)`（对 nil map 的循环是空操作，
等于保留旧配置）与落盘，把「什么都没改」当成功返回给面板。数组/标量则由 unmarshal
直接报错，无需额外判。

测试补在既有 `saveconfig_guard_test.go`：`null`/`[]`/`"x"`/`7` 四种都必须报错、
且**原配置文件一字未改**；`null` 单独断言错误语义（它是唯一 unmarshal 不报错的形态）。

## 怎么验证

| 验证 | 命令 | 结果 |
|---|---|---|
| 守卫用例 | `go test ./cmd/server -run TestSaveConfigRejectsBadInputWithoutWriting` | ok |
| 牙齿 | 守卫改成 `if false && ...` 后复跑 | **FAIL**（且因测试传 nil 依赖而 nil-deref，证明守卫挡在危险动作之前） |
| 包级 | `go test ./cmd/server` | ok |
| 格式 | `gofmt -l ./cmd ./internal` | 空 |

## 未验证

生产上未用真实 `null` body 打面板保存接口——该接口会落盘，即使当前判据下是空操作
也不值得为此动生产配置。结论仅来自单测。

## 回滚

删掉这 3 行守卫即可；行为回到「null 被当合法提交、原样重写配置并报成功」。
