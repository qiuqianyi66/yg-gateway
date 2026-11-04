---
last_updated: 2026-11-04
status: active
owner: YG
source_repo: magpie（本地 clone，见 docs/reference/magpie-rea-full-2026-11-03.md）
---

# magpie 全盘 REA 借鉴对照（2026-11-04）

> 全量证据在 `docs/reference/magpie-rea-full-2026-11-03.md`（9 管线整合，532 行）。
> 本页是**治理层沉淀**：五面风控结论 + ban-risk 三条对照 + 可借鉴清单落地状态。
> 标记语义沿用 AGENTS.md 四态：`observation` 直接读到 / `derivation` 算出 /
> `inference` 样本外推 / `unknown` 未覆盖（写明 requires）。

## 0. 取材边界

- REA 对象：magpie 源码（`internal/gateway`、`internal/provider`、`netproxy.go`
  等，行号口径见 reference 文档）。
- 对比基线：yg HEAD=`081e200`（RPM 硬闸落地后）。

## 1. 五面风控结论（yg 落地映射）

| 面 | magpie 做法 | yg 状态 | 落点 |
|---|---|---|---|
| 身份面（会话不编造） | `gateway.go` sessionOf 全空返 ""，`conversations.go:22-23` "never makes one up (#1355)"；UA 伪装随官方版本自动更新 | ✅ 已符合（🔵-1，无需改动） | conversations.go:22-23 |
| 节奏面（出站硬闸+复活） | 60s+1s 滑动窗 RPM 硬闸（rpm.go:44）；429 分档退避；/v1/models 零成本探测复活；refresh 8 天错峰；每日 warm-up | ✅ **机制已落地**（P1-1B，081e200）：rpmRoundTripper 按账号分桶；429 五分类**闭合不细化**（13 类 kind 已覆盖）；探测复活 yg 已有 breaker_halfopen | upstream/rpm.go |
| 凭据面（三层锁） | 进程 RWMutex + `.renew.lock` + `.run-*.lock` 单 holder | ✅ 不排期：单实例单进程无并发写凭证；**yg Windows ACL 反而更扎实** | 规划 P2 留档 |
| 出口面（显式代理 fail-closed） | netproxy.go:71-73 choice 非空不回退直连；dial 失败冒泡换号 | ✅ **已落地+部署**（P0-1 316c0e7 语义 + P0-2 6bef0d2 观测） | transport.go + /status |
| 平台隔离面 | `MAGPIE_NO_ACCOUNTS=1` 单 gate；X-Magpie-* 前缀黑名单挡 vendor 外 | ✅ yg 头构造式白名单（🟢-2）已覆盖等价语义 | headers.go:173-192 |

## 2. ban-risk 三条对照（#11/#12/#13）

| 待办 | magpie 怎么做 | yg 怎么做 | 状态 |
|---|---|---|---|
| #11 代理 fail-closed | 显式 choice 即 fail-closed，dial 错误冒泡驱动换号 | 哨兵 errProxyConfig + failDial 冒泡 rotateBackoff；直连不动 | ✅ 已实施+部署（P0-1/P0-2） |
| #12 11102 三值判定 | 429 五分类词表 + probeOne 只探最早解封 | 模型目录三值判定（4fb5639 + P0-3 e2e）+ 半开探测；429 不细化 | ✅ 已实施 |
| #13（如适用） | — | 见 REA 文档 ⑤ 节 | 见 reference |

## 3. 可借鉴清单落地状态

- P0 出站 RPM 硬闸 → ✅ P1-1B `081e200`（机制；默认 0 关，与校准结论解耦）
- P0 warm-up 错峰范式 → ✅ 不排期（yg scheduler 签到/token 保活等价；养号走正常客户端路径 R8）
- P1 面板鉴权组合拳 → ✅ P1-3 `b544037`（Bearer 非 cookie 审计 → api() 强制机制化）
- P2 PII 确定性占位符 → ✅ 闭合不适用（prompt_cache_key 已确定性哈希派生）
- P2 三层锁 → ✅ 留档不排期（单实例单进程）
- P3 更新签名纪律 → ✅ 留档（yg 无自更新；若做必须 Authenticode，Windows 裸 SHA256 是反面教材）

## 4. 本页维护

- 新借鉴落地后更新第 3 节状态列（commit + 落点）。
- 未知项不在此登记（走 internal/unknown/ registry，见 AGENTS.md）。
