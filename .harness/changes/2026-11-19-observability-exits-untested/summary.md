# 观测出口的两个零覆盖函数（KeyRPMStats / promptTooLongMessage）

日期：2026-11-19
提交：`b52891b`
上一提交：`4e46d6f`

## 一、为什么只有这两个

剩余零覆盖 5 个，先逐个判价值再动手：

| 函数 | 判断 | 处置 |
|---|---|---|
| `KeyRPMStats` | 两个真实分支（nil / 快照委托） | **补** |
| `promptTooLongMessage` | 两个真实分支（原文 / 兜底） | **补** |
| `formatInt` | `strconv.FormatInt` 一行包装，补它等于测标准库 | 不补 |
| `Unwrap` | `return o.ResponseWriter` 一行，同上 | 不补 |
| `SetChatLogOutput` | `chatLogOut = w` 一行，仅 main 启动期调一次 | 不补 |

后三个是**纯转发**，补测试只能证明「转发语句存在」，不产生新知识。这三个明确不补，
写在这里以免下一轮有人重复评估。

## 二、KeyRPMStats：nil 与空数组是两种语义

```go
func (h *Handler) KeyRPMStats() []map[string]any {
    if h.keyRPM == nil {
        return nil
    }
    return h.keyRPM.Snapshot()
}
```

`keyRPM` 只在 `NewHandler` 里构造（`handler.go:262`），注释把 nil 分支标成「理论不可达」。
但零值 `&Handler{}` 在测试与部分构造路径里真实存在，nil 解引用会让
`/panel/api/overview` 直接 500 —— 这个守卫不是形式主义。

**两条用例分别钉住两种返回**：

| 用例 | 断言 |
|---|---|
| `TestKeyRPMStatsNilGuard` | 零值 Handler → **nil** |
| `TestKeyRPMStatsExposesSnapshot` | NewHandler → **非 nil** 且长度 0 |

**为什么 nil 与空数组必须分开**：`nil` = 「没有观测器」，`[]` = 「观测器在、观测到 0 个 key」。
面板据此决定显示「无数据」还是「0 请求」。如果这里返回空切片代替 nil，二者就混了。

## 三、promptTooLongMessage：兜底也不许伪造上游原文

```go
func promptTooLongMessage(body string) string {
    if strings.TrimSpace(body) == "" {
        return "prompt is too long"
    }
    return body
}
```

上游 11115 的 body 原文里有**真实 token 数、上限值、requestId**，是客户端排查超限的
唯一线索。网关在这里**只做一件事**：body 为空时兜一句可读短文案。

用例两条分支：
- 原文原样透传，且断言 `2100000` / `2000000` / `11115` 三个关键信息都还在（防吞）
- 纯空白 body → 固定短文案，且断言兜底文案**不含 `{` 或 `code`**（不得伪造 JSON 信封冒充上游）

第二条是「不许编造证据」在代码层面的一个具体落点。

## 四、变异验证

变异：`if strings.TrimSpace(body) == ""` → `if ... && false`（兜底永不触发）

```
--- FAIL: TestPromptTooLongMessage/纯空白_body_兜底为可读短文案
```

恢复后 `ok`。**这条变异有效**。

（第一次尝试用 `body == ""` 替换 `strings.TrimSpace(body) == ""`，结果 `strings` 变成
未使用导入 → **编译失败**。编译失败不是测试失败，不能算变异被抓住；换成 `&& false`
才是真正的行为变异。）

## 五、过程中踩的一个坑（PowerShell）

用 `-replace` 注入 `&&` 时写了 `\&\&`，PowerShell 单引号字符串不认反斜杠转义，
结果 Go 源码里出现字面 `\`：

```
internal\server\helpers.go:63:35: invalid character U+005C '\'
internal\server\helpers.go:63:40: invalid operation: cannot take address of false
```

教训：**PowerShell 单引号字符串里不要为 `&&` 加反斜杠**。这与 LESSONS L-3
（native 命令 `--flag=$var` 不展开变量、生成字面名文件）是同一类坑：PowerShell 不做
我们以为它做的事，且失败信息指向 Go 而非 PowerShell，容易误判。

## 六、覆盖率

| 函数 | 改前 | 改后 |
|---|---|---|
| `KeyRPMStats` | 0.0% | **100.0%** |
| `promptTooLongMessage` | 0.0% | **100.0%** |
| `internal/server` 包 | 83.7% | **83.9%** |

## 七、能力边界

1. `KeyRPMStats` 只验「能拿到快照」，未验快照内容的正确性 —— 那属 `keyrpm.go`
   内部语义，已由 `keyrpm_test.go` 的 5 个用例覆盖。
2. `promptTooLongMessage` 未走 11115 端到端链路（上游真返回超限 → 客户端收到原文）；
   该链路由既有错误路径测试间接覆盖。
3. `formatInt` / `Unwrap` / `SetChatLogOutput` 保持零覆盖，**明确不补**，理由见第一节。

## 八、回滚

```powershell
git revert --no-edit <本批 sha>
```
