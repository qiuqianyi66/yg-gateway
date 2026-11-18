# fetchGlobalModels：补测试，并诚实记录一次失败的变异验证

日期：2026-11-18
提交：`<待填>`
上一提交：`636f316`

## 一、函数作用

`internal/server/models_handler.go:449`

```go
func (h *Handler) fetchGlobalModels() ([]string, *auth.Auth) {
    acct := h.cfg.Pool.PickExcludingForRealm(nil, "", "global")
    if acct == nil {
        return nil, nil
    }
    return h.cfg.Upstream.FetchGlobalModels(acct), acct
}
```

注释写的契约：**池中无 global 账号 → 空名单 + nil 账号（零上游调用）**。

为什么值得测：纯 CN 池的部署里，每次 `/v1/models` 刷新都会走进这里。若空池时白打上游，结果必然为空——纯浪费，还污染上游错误统计。

## 二、补的用例

`TestFetchGlobalModelsEmptyPoolMakesNoUpstreamCall` —— 空池，三断言：
- 名单为 nil（**不是空切片**：调用方用 `names == nil` 区分「没探测」与「探测为空」）
- 账号为 nil
- fake upstream 的调用计数 **== 0**

## 三、变异验证失败（必须记录）

按惯例删掉 `if acct == nil { return nil, nil }` 做变异：

```
ok  	yg-gateway/internal/server	0.061s     ← 测试仍然通过
```

**变异没有被抓住。** 定位原因（不是猜测，是读了代码）：

```go
// internal/upstream —— fetchGlobalModelsOnce 第一行
func (c *Client) fetchGlobalModelsOnce(a *auth.Auth) (names []string, infos []ModelInfo) {
    if !c.globalOn(a) {
        // 逃生门兜底：账号不路由 global 上游 → 不探测（零上游调用）
        return nil, nil
    }
    ...
```

删掉本地守卫后：
- `PickExcludingForRealm` 在空池仍返回 nil → `acct == nil`
- `FetchGlobalModels(nil)` → `globalOn(nil)` 为 false → **直接返回，零上游调用**

端到端行为**完全等价**。所以我的用例守的是「整体不打上游」这条总契约，
**不是** `fetchGlobalModels` 里这一行。

这是**两层 nil 判断功能重叠**，不是测试写法有问题：这一层的守卫确实冗余。

## 四、为什么仍然保留这个守卫与这条测试

**守卫保留**：本地短路比「依赖跨包另一层的 nil 处理」更清晰，且注释把「零上游调用」
写成这一层的职责。删掉它会让这条契约悄悄转移到 `upstream` 包，读者在那个文件里
看不到为什么 `fetchGlobalModels` 不需要判 nil。

**测试保留**：它守的是调用方真正关心的端到端事实（空池刷新模型列表不会打上游）。
变异抓不住它，是因为这个事实由两层共同保证 —— **这是重叠防御，不是测试失灵**。
记录在这里，避免下一个人误以为这条用例能钉住那一行。

**不宣称「测试有牙齿」**：本批只有「已实现」「Harness 已验证」，「变异已验证」这一项
是**失败**的，如实标注。

## 五、覆盖率

| 项 | 值 |
|---|---|
| `fetchGlobalModels` | 0.0% → **75.0%** |
| `internal/server` 包 | 83.5% → **83.7%** |

**75% 而非 100% 的原因**：未覆盖分支是「池中有 global 账号 → 真的探测」。走那条分支
需要构造一个 realm=global 的账号，而 `Auth.realm` 是私有字段，构造需 `BackfillRealmFor`
配合 `SetGlobalEnabled(true)` 这个**进程级 atomic 全局开关**（测试后必须恢复，否则污染
同包其它用例）。且该分支的实际行为（探测 + 缓存 + 负缓存）已由 `internal/upstream`
的 4 个 `FetchGlobalModels*` 用例完整覆盖。

**这是有意的取舍**：成本（改全局开关 + 私有字段构造）高于价值（上游侧已验）。

## 六、能力边界

1. 只测空池路径，global 账号的真实探测未在此层验证（理由见上）。
2. 未验证 `/v1/models` 端到端在纯 CN 池下的输出形状（由既有 models handler 测试间接覆盖）。
3. 删除了一版我最初写的两条用例（nil Pool 不 panic、nil vs 空切片）—— 前者属配置缺失
   场景、超出一批范围；后者与主用例重复。**宁少不滥**。

## 七、回滚

```powershell
git revert --no-edit <本批 sha>
```
