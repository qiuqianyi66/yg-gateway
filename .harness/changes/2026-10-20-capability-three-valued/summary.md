# capability-three-valued 变更记录

> 需求来源：`docs/code-review/2026-10-20-four-dimensions.md` §一 [P3] R10 静默解析
> （`global_models.go:645-656`）+ AGENTS.md 顶层硬约束「缺失 ≠ 否定」。
> 复核时发现它**比 P3 严重**：AGENTS.md 声称「面板已按此修正」的能力三值处理，
> 在生产链路里**从未真正生效**。

## 一、变更概览

| 项 | 内容 |
|---|---|
| 功能名 | capability-three-valued（模型能力字段三值语义贯通） |
| 类型 | fix + test |
| 涉及范围 | `internal/upstream`（client.go / global_models.go / hint.go）、`internal/server/handler.go`、`internal/panel/panel.go` + 4 个测试文件 |

## 二、问题：三值在**序列化层**被压扁

证据链（每一环都有实测位置）：

1. `upstream/client.go` `ModelInfo.SupportsImages` / `SupportsReasoning` 是普通 `bool`
   → **无法表达三值**。
2. `upstream/global_models.go:646` `mi.SupportsImages, _ = obj["supportsImages"].(bool)`
   → 上游省略该键时直接落成 **false**（CN 侧 `dynModelEntry` bool 字段同理）。
3. `panel/panel.go` **无条件** emit `"supports_images": mi.SupportsImages`
   → 永远 true/false，**从不省略**。
4. `panel/app.js:1060` `if (f.cap === 'vision' && m.supports_images === false) return false;`
   → 未知能力模型被当成显式 false，**照样被筛掉**。

结论：前端那道 `=== false` 的修正在生产链路里从未生效——后端总发显式 false。
后果正是 AGENTS.md 警告的「用户按视觉筛选时实际支持图片的模型会消失，且不报错」。

**两个 producer 口径不一致**：`server/handler.go`（`/v1/models`）只在 true 时 emit，
于是「显式不支持」与「未知」都表现为省略，两者也无法区分。

## 三、方案

`ModelInfo` 的两个能力字段（及 `dynModelEntry` 对应 JSON 字段）改为 `*bool`：

| 上游形态 | ModelInfo | 透出 |
|---|---|---|
| `supportsImages: true` | `*true` | `true` |
| `supportsImages: false` | `*false` | `false` |
| 键缺失 / 值非 bool / 断言失败 | `nil` | **字段省略** |

- 解析侧：只在 `obj[k].(bool)` 的 `ok` 为真时才赋指针，其余保持 nil。
- `/v1/models`（handler.go）：`*true→true`、`*false→false`、`nil→省略`，三值可区分。
- 面板 `panelModelEntry`：nil 必须省略（这是当年 bug 的**根因所在层**）。
- `upstream/hint.go`：`ModelSupportsImages` 改 `*bool`，`GatewayHint` 只在
  **显式 false** 时才下「该模型不支持图片，请换模型」的判定；nil 走中性提示。
  ——这一步让实现追上了它自己注释里早就写明的意图
  （「目录能对该模型做出『不支持』判定时才给指向」）。

**刻意不动**：`SupportsToolCall` / `OnlyReasoning` / `CanDisableThinking` / `IsDefault`
仍是普通 bool（AGENTS.md 与前端都只要求 images/reasoning 三值，避免过度扩张改动面）。

## 四、测试：补上当年缺失的那一层

- **`internal/panel/model_entry_threevalued_test.go`（新增，关键）**：
  直接断言 `panelModelEntry` 的序列化三态。
  *为什么必须钉这一层*：原 `frontend_test.go:465 TestAppJSModelFilterUnknownCapability`
  是**直接把 `{id:'cn:unknown'}` 喂给 JS 函数**，绕过了 `panel.go` 序列化 →
  **测试绿、生产坏**。这次补的正是当年缺的那层守卫。
- **`internal/upstream/threevalued_cap_test.go`（新增）**：`parseGlobalModelLoose` 四态
  （显式 true / 显式 false / 键缺失 / 值非 bool）+ `GatewayHint` 未知不得断言不支持。
- 既有断言 9 处编译级破坏点改用 `capTrue` helper：**nil 与 false 都算失败但原因分开报**
  （若把 nil 当 false 处理，等于在测试里重演被修掉的 bug）。
- 提示测试表补 1 例：「目录未声明(nil) → 中性提示」。

## 五、自检清单

- [x] `go build ./...` + `go vet ./...` 通过
- [x] `go test ./...` **20 个包全绿**
- [x] gofmt 干净
- [x] 牙齿验证（panel 序列化层）：退回无条件 emit → 3 条断言精确 FAIL
      （未声明却出现键 / true 变指针 / false 变指针）；还原 → PASS
- [x] 三值用例覆盖：true / false / 键缺失 / 值非 bool 四态各自独立断言

## 六、Learning

根因不在「少写一个 if」，而在**测试的攻击面错位**：前端三值逻辑被 JS 单测覆盖了，
但把 unknown 压成 false 的是**上游的 Go 序列化层**，那一层当时没有任何测试。
于是单测通过、契约文档（AGENTS.md）也写得很对，生产仍然是坏的——
**文档正确 ≠ 实现正确 ≠ 被测试钉住**，三者都要各自验证。
判定同类问题的方法：顺着数据流的每一层问「这一层有没有测试直接钉住它」，
而不是问「这个功能有没有测试」。
