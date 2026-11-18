# 请求解析边界形态（resolveModel / stripRateSuffix / hasImagePart）

日期：2026-11-18
提交：`<待填>`
上一提交：`fa9e304`

## 一、先纠正一处我自己的误判

查零覆盖清单时我看到 `resolve_model.go:38 ResolveModel 0.0%`，第一反应是「路由核心零覆盖」。
实际读 profile 后：

```
resolveModel 85.7%   ← 真正的实现，已覆盖
ResolveModel 0.0%    ← 只是一行导出包装 { return resolveModel(model) }
```

把「导出包装零覆盖」当成「实现零覆盖」会把工作花在一个空壳上。
`ResolveModel` 之所以 0%，是因为包内测试调 `resolveModel`，要覆盖它得写包外测试 ——
而它没有任何逻辑，补测试的价值约等于零。**明确不补**。

真实的缺口是 `resolveModel` 的 14.3% 和 `stripRateSuffix` 的 33.3%，也就是两个分支。

## 二、`resolveModel` 缺的那条分支为什么重要

```go
prefix := model[:idx]
if prefix != "cn" && prefix != "global" {
    return "cn", stripRateSuffix(model)     // ← 这条就是缺的分支
}
```

它是**「只认两个枚举前缀，其余带冒号的模型名一律当裸名」**这条规则的全部实现。

模型名带冒号是真实存在的（`ollama:llama3`、`account:model`）。如果这条分支被写成
「剥任意前缀」，那么 `ollama:llama3` 会变成 `llama3` —— 上游收到一个它不认识的模型名，
网关不报错，选号后 400。**静默改坏请求**，且客户端看到的错误信息会指向模型不存在，
排查方向从「网关改名」变成「模型不支持」，是误导性的。

大小写敏感同属这条规则：`CN:hy3` 的 prefix 是 `CN`，不等于 `cn`，因此按裸名处理
（`bare = "CN:hy3"`）。注释写明「前缀必须是精确的小写枚举」。

## 三、`hasImagePart` 的 0% 意味着回落路径从未被执行

`hasImagePart` 是 `chatRun.hasImage()` 的**回落分支**：

```go
// chatrun.go:116-120
if r.imageViewOK {
    r.imageVal = hasImageView(r.imageReqView)   // 主路径，60% 覆盖
} else {
    r.imageVal = hasImagePart(r.imageRawBody)   // 回落，0% 覆盖 ← 从没跑过
}
```

主路径 `hasImageView` 有覆盖，回落路径没有。这正是「只验正向路径会漏掉半成品」的典型：
两条分支里只有一条被证明能工作。

判据「宁缺勿滥」写在注释里：判不出带图就不给「模型不支持图片」的提示。
所以畸形形态必须一律 false，而不是猜 true —— 猜 true 会让一个纯文本请求收到
「你不该发图」的错误提示。

## 四、补的用例

| 用例 | 子用例数 | 守什么 |
|---|---|---|
| `TestResolveModelForeignColonPrefixIsNotRealm` | 7 | 无冒号 / cn / global / 外来前缀视为裸名 / 大写 CN / 混写 |
| `TestResolveModelStripsRateSuffix` | 4 | 倍率后缀剥离（裸名 / cn / global / 旧客户端无后缀） |
| `TestHasImagePartContract` | 6 | image_url → true；纯文本 / 字符串形态 / 不可解析 / 无 messages → false |

## 五、变异验证

把 `if prefix != "cn" && prefix != "global"` 对话框整段删掉（等价于"总是剥离前缀"）：

```
--- FAIL: TestResolveModelForeignColonPrefixIsNotRealm/其他冒号前缀视为裸名
--- FAIL: TestResolveModelForeignColonPrefixIsNotRealm/带空格的外来前缀也算裸名
--- FAIL: TestResolveModelForeignColonPrefixIsNotRealm/大写_CN_不算_realm
```

3 个子用例同时抓住。恢复后 `ok`。

注意：三条**全部**落在「外来前缀」这条规则上，说明这个变异只被该规则的断言拦住 ——
这也验证了「其它断言确实在守各自的分支，没有互相顶替」。

## 六、覆盖率

| 函数 | 改前 | 改后 |
|---|---|---|
| `resolveModel` | 85.7% | **100.0%** |
| `stripRateSuffix` | 66.7% | **100.0%** |
| `hasImagePart` | 0.0% | **100.0%** |
| `internal/server` 包 | 83.1% | **83.5%** |

## 七、能力边界

1. `ResolveModel`（导出包装）仍 0%，**明确不补**——它没有逻辑。
2. 未走 `/v1/chat/completions` 端到端验证「`ollama:llama3` 这类名字真的会到上游」；
   `resolveModel` 是纯函数，衔接由既有 chatrun 测试间接覆盖。
3. `hintContext` 仍 44.4%，本批未动 —— 它需要构造完整 gate 判定上下文，属另一类
   （集成而非纯函数），单独立项。

## 八、回滚

```powershell
git revert --no-edit <本批 sha>
```
