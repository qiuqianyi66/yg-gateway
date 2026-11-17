# 变更：发布 YG Gateway v2.2.0

- 日期：2026-11-17
- 分诊：L2（发布动作，涉及 tag / 版本串 / 外部产物）
- 基线：`main` @ `9c75b08`，tag `v2.2.0`

## 本版装了什么

三轮降体量 + 一轮安全加固 + 一轮分配优化 + 文档/品牌资产：

| 提交 | 内容 |
|---|---|
| `1a1acbd` | `pool.Pick` 180 代码行 → pick / selectFromCandidates / commitPick / maxCreditsOf |
| `554f04f` | `Aggregate` 211 代码行 → 26 行读取循环 + `sse_aggregate.go` 的 sseAggregator |
| `21b0ee4` | `main()` 228 代码行 → prepareConfig / buildObservability / buildScheduler / buildSessionRouter / buildHTTPRuntime |
| `5dc3ca0` | API 侧补 `nosniff` + GET `no-store`（`/status`、`/metrics` 此前无安全头，可被中间代理缓存绕过鉴权） |
| `41d96f6` | 合并 `costTier`/`cost1k` 平行切片，Pick 5→4 allocs/op（5.2–5.4µs → 5.0–5.1µs） |
| `2cfddaf` | README 补 TL;DR 摘要块与「常见叫法」，GitHub topics 12→16 |
| `91a47f6` | 新增 1280×640 品牌横幅，README 顶部引用 |
| `9c75b08` | `appVersion` 2.1.0 → 2.2.0 |

体量指标（`.harness/depth-baseline.json`，本轮抬高三次）：
`maxFileLines 942 → 931`、`oversizeFuncs 3 → 0`、`minCoverage 75.5% → 78.4%`。

## 发布结果（实测）

`git push origin v2.2.0` → CI `release` workflow（run `44032675491`）：

| job | 结论 |
|---|---|
| test / gate / race | **success** |
| build (windows amd64 / linux amd64 / linux arm64 / darwin amd64 / darwin arm64) | **success**（5/5） |
| publish | **success** |
| build-image（ghcr 多架构镜像） | **failure** |

Release `v2.2.0` 已产出（非 draft、非 prerelease）：

```
checksums.txt
yg-gateway-v2.2.0-windows-amd64.zip   4.49 MB
yg-gateway-v2.2.0-linux-amd64.tar.gz  4.29 MB
yg-gateway-v2.2.0-linux-arm64.tar.gz  3.87 MB
yg-gateway-v2.2.0-darwin-amd64.tar.gz 4.36 MB
yg-gateway-v2.2.0-darwin-arm64.tar.gz 4.01 MB
```

CI 自带的硬断言全部通过：源码 `appVersion` 与 tag 一致、二进制内含版本串（5 个平台
逐个 `grep -qa`）。

## 未解决：ghcr 镜像推送失败（**不属于本次改动引入**）

失败信息：

```
ERROR: failed to push ghcr.io/qiuqianyi66/yg-gateway:2.2.0:
  denied: permission_denied: write_package
```

**证据链（这不是本次才坏的）**：

- `gh api user/packages/container/yg-gateway` → `version_count=378`，
  `updated_at=2026-10-24T09:35:59Z`（正好是 v2.0.0 发布那天，之后没再新版本）。
- `gh run list --workflow release --limit 8` → 最近 8 次 release run **全部 failure**，
  最早一次 2026-11-17T04:54（在本轮任何改动之前）。
- 仓库 `actions/permissions/workflow` 已是 `default_workflow_permissions: write`，
  所以不是 GITHUB_TOKEN 的默认权限问题。
- 推断（`inference`）：包 `yg-gateway` 是旧仓库时代创建的（workflow 注释里仍留着
  `ghcr.io/linguo2625469/workbuddy2api-panel` 字样），仓库改名/转移后，
  **包与该仓库的 Actions access 关联失效**，`GITHUB_TOKEN` 不再被视为有权写这个包。

**为什么没有自动修**：GitHub 的包访问控制 API 只对 organization 开放
（`/orgs/{org}/packages/.../access`），**用户级包没有对应的 REST 端点**，只能网页操作。

**两条可选修复（需你决定，二选一）**：

1. **网页授权（推荐，不改代码）**：仓库右侧 Packages 区 → `yg-gateway` →
   *Package settings* → *Manage Actions access* → 添加 `qiuqianyi66/yg-gateway`，角色选
   **Write**。加完重跑 `gh run rerun 44032675491 --failed` 即可。
2. **换包名（改 workflow 一行）**：把 `IMAGE_NAME` 改成 `yg-gateway-ghcr` 之类的新名字——
   新包首次由 Actions 创建时自带写权限。代价是镜像地址变，旧包历史断开。

**影响面**：只影响 Docker 镜像用户。Release 二进制、`go install`、源码构建三条路径都不受影响。

## 验证方式（可复现）

```powershell
gh release view v2.2.0 --json name,isDraft,isPrerelease,assets
gh run view 44032675491 --json status,jobs
gh run list --workflow release --limit 8 --json databaseId,headBranch,conclusion
gh api user/packages/container/yg-gateway
```

## 回滚

- Release 层面：`gh release delete v2.2.0 --yes` + `git push origin :refs/tags/v2.2.0`
  （**注意**：删 tag 会让已下载的包失去来源，非必要不回滚）。
- 版本号层面：`appVersion` 改回 2.1.0 并重新打 tag。
