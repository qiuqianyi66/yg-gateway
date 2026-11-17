# 覆盖率棘轮逐包守护（修棘轮盲区）

日期：2026-11-17
类型：工程化 / 门禁有效性
轨道：E 工程化与 Harness

## 一、缺陷（改前证据）

上一批把 `internal/server` 覆盖率从 78.5% 提到 80.5%，跑 `check-depth.ps1 -Update` 后
发现**这个提升根本不受棘轮保护**。

原因在脚本实现（`scripts/check-depth.ps1` 改前 293-305 行）：

```powershell
$coverages = @()
foreach ($line in $coverOut) {
    if ($line -match 'coverage:\s+([\d.]+)%\s+of statements') {
        $pct = [double]$Matches[1]
        if ($pct -gt 0) { $coverages += $pct }   # ← 只留百分比，包名被丢掉
    }
}
$minCoverage = ... (Minimum) ...
```

它只保留所有包的百分比，取最小值写进 `minCoverage`，**包名在收集时就被丢弃**。于是：

- 棘轮守护的是「最差的那个包别更差」（当前 78.7 = `internal/alerting`）；
- **任何非最差包退步都不会红** —— 把 `internal/server` 从 80.5% 降回 79% 只能靠人眼发现。

这让门禁在覆盖率维度上**给出虚假的安心感**：它让人以为"覆盖率有守护"，
实际只守住了 27 个包里最差的那 1 个。

## 二、改了什么

`scripts/check-depth.ps1` 四处：

1. **收集时保留包名**：新增 `$packageCoverage` 有序字典，从 `^ok\s+(\S+)` 取包路径，
   只记覆盖率 >0 的包（与 `minCoverage` 同口径：`?  pkg  [no test files]` 天然排除，
   否则 0 覆盖包会让守护恒红）。
2. **纳入 `$current`**：`packageCoverage = $packageCoverage`，
   于是 `-Update` 自动把它写进 `.harness/depth-baseline.json`。
3. **逐包比较三个判据**：
   - 包还在且 `当前 >= 基线 - 0.5（COVERAGE_TOLERANCE）`；
   - **基线记过的包不允许消失**（包被删 / 测试被删时它会从报告里消失，
     只看"当前存在的包"会把这个洞放过去）；
   - 新出现的包不判回退（纳入基线是 `-Update` 的事）。
4. **输出与提示**：度量段打印「逐包覆盖率守护 : N 个包」；变好时提示可 `-Update`。

向后兼容：老基线（如 `v2.2.x` 之前那份）没有 `packageCoverage` 字段时
`$null -ne $basePkg` 为 false → 逐包检查整段跳过，不会让旧分支直接失败。

## 三、验证（牙齿，两类都实测红）

用 `-CoverageStdout` 造 mutant，避免污染仓库与重跑测试。

**基线值**：`internal/server` 80.5%（27 个包入册）。

| 场景 | 构造方式 | 期望 | 实测 |
|---|---|---|---|
| ① 包覆盖率下降 | 把真实输出里 `internal/server` 的 80.5% 改成 79.0% | exit 1 | **exit 1**，`REGRESSED: 包覆盖率 yg-gateway/internal/server 80.5% → 79%` |
| ② 包从报告消失 | 整行删掉 `ok  yg-gateway/internal/server ...` | exit 1 | **exit 1**，`… 80.5% → 无覆盖率数据（包消失或测试被删）` |
| ③ 真实数据不误报 | 直接用真实 `go test ./... -cover` 输出 | exit 0 | **exit 0**，`✅ 深度指标未回退` |

三类都在同一份脚本上跑，说明守护不是"只在 mutant 里生效"。

## 四、能力边界

- 容差仍是 `COVERAGE_TOLERANCE = 0.5`，即单包下降 0.5pp 以内不报。
  这是既有约定（避免浮点抖动误报），本轮沿用，**不是漏洞而是有意噪声容忍**。
- 只守护**有测试的包**。`cmd/bench`、`cmd/harnessrun`、`cmd/signin`、`cmd/trial`
  四个 0.0% 覆盖的包仍不在守护范围内（候选 E1，未做）。
- 下次 `-Update` 会把当时新出现的包也写进基线；如果有人用 `-Update` 掩盖退步，
  `-Update` 本身不阻止（既有设计：`-Update` 是显式放水动作，靠 review 看 diff）。

## 五、回滚

```powershell
git checkout -- scripts/check-depth.ps1 .harness/depth-baseline.json
```

---

## 六、第二轮：`minCoverage` 判定语义修正（同一主问题）

### 6.1 暴露过程

给 `cmd/bench` 补测试（0.0% → 47.9%）后跑 `-Update`，门禁直接报：

```
❌ 深度指标回退（1 项）：
   REGRESSED: minCoverage 78.7% → 47.9%
```

**补测试被判成退步**。这不是偶发，是 `minCoverage` 定义的结构缺陷。

### 6.2 根因

`minCoverage` = 「所有覆盖率 >0 的包的**最小值**」。它在两个方向都会给出错误结论：

| 方向 | 场景 | 错误结论 |
|---|---|---|
| ① 假回退 | `cmd/bench` 0.0% → 47.9%（补测试） | minCoverage 78.7% → 47.9%，进步被判成退步 |
| ② 放水 | `-Update` 把 47.9% 写进基线 | 门槛被永久拉低，之后的守护以 47.9 为准 |

① 的后果最严重：**它会逼人用 `-Update` 放水才能提交**，正好反噬棘轮自身的设计目的。

### 6.3 处置

**把 `minCoverage` 移出回退判定**，判定只由 `packageCoverage` 负责；保留其计算与打印（信息价值），
打印时标注「信息值，不判红；判定见逐包守护」。

**这不是"为了变绿而删检查"**，理由是冗余而非放水：

`packageCoverage` 逐包判定**严格强于** `minCoverage` —— 每个包都不许退步 ⊃ 最差包不许退步。
任何 `minCoverage` 退步必然伴随某个具体包退步，必然已被逐包检查抓到。
逐包检查还多覆盖两种 minCoverage 抓不到的情形：包消失（"无覆盖率数据"）、任意非最差包退步。

### 6.4 验证

| 判据 | 结果 |
|---|---|
| `-Update` 后正常跑 | **exit 0**，`✅ 棘轮已抬高`，逐包守护 28 个包 |
| `cmd/bench` 已入册 | 基线 `packageCoverage` 含 `yg-gateway/cmd/bench: 47.9` |
| 逐包回退仍能拦 | 见第三节两类 mutant，仍在 `packageCoverage` 分支上（该分支未改动） |

