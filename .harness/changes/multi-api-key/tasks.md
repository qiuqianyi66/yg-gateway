---
last_updated: 2026-10-11
status: 全部完成（T0~T7 + 最终审查 4 阻断项修复）
owner: YG
adr: docs/adr/ADR-001-multi-api-key.md
prd: docs/prd/REQ-yg-gateway-multi-api-key-2026-10-11.md
---

# 多 API Key 派发 — 任务拆分

> 十阶段流水线阶段 3 output。按 ADR-001 固化契约拆分，每任务 ≤4h。
> 顺序：**先测试 → 再核心 → 再接线 → 最后全链路验证**（ADR 第三节指定）。

---

## 依赖图

```
T0 基线回归（现有测试全绿）
 └─► T1 httpauth 多 Key 原语 + 前缀索引（核心）
      └─► T2 config 解析 + api_keys 字段 + 三态反序列化
           └─► T3 livecfg 快照扩展 + 原子热更新
                └─► T4 handler/panel withAuth 接线
                     └─► T5 请求上下文透传（key_id/key_name）
                          └─► T6 realm + models 约束校验（选号前）
                               └─► T7 端到端 + 并发热更新 + benchmem
```

---

## 任务清单

### T0 基线回归（0.5h）

- [ ] 跑现有全量测试，确认基线全绿（`go test ./...`）
- [ ] 记录基线：`httpauth` 现有测试通过、`go vet` 无输出
- **产出**：基线快照（不做任何代码改动）

### T1 httpauth 多 Key 原语 + 前缀索引（3h）

- [ ] 新增 `Key` 结构：`ID/Name/Digest/Prefix/Models/Realm`（运行时形态，不含明文）
- [ ] 新增 `Index` 结构：`map[prefix][]*Key`（碰撞桶，不覆盖）+ 单值 fallback
- [ ] `BuildIndex(keys []Key) *Index`：计算摘要 + 前缀 + 建桶
- [ ] `Match(r *http.Request) (*Key, bool)`：前缀定位 → 桶内常量时间比完整摘要
- [ ] 保留 `VerifyBearer` 旧签名（单 Key 兼容路径委托给 Index 的单值形态）
- **测试（先行）**：旧兼容回归（现有测试不动全绿）、前缀碰撞、重复 key、空索引
- **门禁**：`go test ./internal/httpauth/` 全绿

### T2 config 解析 + api_keys 字段 + 三态反序列化（3h）

- [ ] `Config` 加 `APIKeys []APIKeyConfig` + 自定义反序列化（保留「键存在」信息）
- [ ] `APIKeyConfig` 字段：`ID/Key/Name/Models/Realm`
- [ ] `normalize()`：id 必填/不重复/格式校验；key 空条目忽略；realm 枚举校验；
      三态 models 保留（缺失/`[]`/非空）
- [ ] 空值语义：键缺失→兼容；键存在但 null/`[]`/全空 → fail fast
- [ ] `config.example.json` 补 `api_keys` 示例
- **测试（先行）**：六态契约表、重复 id、非法 realm、三态 models、env 不覆盖 api_keys
- **门禁**：`go test ./cmd/server/` 全绿

### T3 livecfg 快照扩展 + 原子热更新（3h）

- [ ] `Snapshot` 加 `APIKeys []httpauth.Key`（运行时形态）+ `APIKey`（单值兼容）
- [ ] `BuildSnapshot`：独立对象完成校验 + 摘要 + 索引构建，失败返回 error
- [ ] `saveConfig` 热应用：构建新快照成功后一次 `atomic.Store`；失败拒绝应用保旧快照
- [ ] 首次启动无有效快照 → 启动失败（fail fast）
- **测试（先行）**：原子切换（-race）、失败保留旧快照、空配置 fail fast
- **门禁**：`go test -race ./internal/livecfg/ ./cmd/server/`

### T4 handler/panel withAuth 接线（2h）

- [ ] `withAuth` 改走 Index.Match；匹配成功拿 Key，失败 401（语义不变）
- [ ] panel `withAuth` 同口径；`apiKey()` 改为走 Index（Live 优先）
- **测试（先行）**：401 语义不变、多 Key 均放行、未知 Key 401
- **门禁**：`go test ./internal/server/ ./internal/panel/`

### T5 请求上下文透传 key_id/key_name（1.5h）

- [ ] 鉴权结果（key_id/key_name）注入请求上下文，不携带明文
- [ ] 日志记录 key_id（短），key_name 供展示
- **测试（先行）**：上下文含 key_id/key_name 且不含明文/Authorization
- **门禁**：`go test ./internal/server/`（含日志脱敏断言）

### T6 realm + models 约束校验（选号前）（3h）

- [ ] 在 handler 鉴权后、选号前执行约束校验
- [ ] realm：复用 `ResolveModel`，规则表（空=不限/cn/global/其他 fail fast）
- [ ] models：三态 + bare 名比对 + 缺 model 边界
- [ ] 错误码：`realm_mismatch` / `model_not_allowed`，400，不泄漏 Key 身份
- **测试（先行）**：realm 四态、models 三态、缺 model、流式+非流式、错误不泄漏
- **门禁**：`go test ./internal/server/`

### T7 端到端 + 并发热更新 + benchmem（3h）

- [ ] 真实处理链路出站报文逐项比对（R8 零变化断言）
- [ ] `-race` 并发请求 + 配置切换
- [ ] `go test -bench` + `-benchmem` 测鉴权耗时与分配（记录环境/Key 数/碰撞桶）
- [ ] 配置保存→重读→热应用链路：`api_keys` 原样保留回归
- **门禁**：全量 `go test -race ./...` + `go vet` + benchmark 记录

---

## 任务完成顺序（ADR 第三节强制）

1. T0（基线）→ 2. T1（httpauth 核心）→ 3. T2（config）→ 4. T3（livecfg 热更新）
→ 5. T4（接线）→ 6. T5（透传）→ 7. T6（约束）→ 8. T7（全链路）

**先测试后实现**：每个任务都是「先写测试（还原 bug 必须 FAIL）→ 再实现 → 测试转绿」。
T1 的旧兼容测试必须先写且保持现有测试全绿，否则不进 T2。
