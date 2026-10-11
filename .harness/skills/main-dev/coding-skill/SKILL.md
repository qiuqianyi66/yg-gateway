---
name: coding-skill
description: 编码实现——Go 单仓库分层（cmd 入口 / internal 领域包）+ 内嵌面板前端，遵守红线 15 条，40% 分批编码批批验证。对应十阶段流水线阶段 4。
---

# 编码技能（YG Gateway）

按 Go 单仓库分层流程实现功能，确保代码质量与架构合规。

## 触发条件

- 需求分析已完成，进入实现阶段
- "实现 xx" / "写一下 xx 的接口" / "改 xx 逻辑"

## 前置条件（硬门，缺失回退）

- ✅ 需求分析已完成（`.harness/changes/{feat}/summary.md` 已存在）
- ✅ 任务已拆分并明确依赖
- ❌ 任一缺失 → 先回 `request-analysis`，禁止硬编码

## 上下文准备（规则 + Wiki ≤4）

| 上下文 | 何时读取 |
|---|---|
| `.harness/rules/编码规范.md` | **必读**（红线 15 条 + 自检清单） |
| `.harness/rules/工程结构.md` | 涉及新建包/模块时（cmd/internal 分层、依赖方向） |
| `.harness/wiki/glossary.md` | 涉及业务术语时 |

## 代码结构参照（禁发明路径）

```
cmd/{server,login,signin,credit,trial}/   # 可执行入口，只有这里能 func main()
internal/
├── auth/        # 凭证解析/原子写回（唯一凭证持有者，加锁方法）
├── httpauth/    # Bearer 鉴权原语（常量时间比较）
├── pool/        # 账号池（选号/冷却/熔断/持久化）★承重墙
├── upstream/    # 上游客户端（唯一出站口，协议/指纹/改写/脱敏）★承重墙
├── server/      # HTTP handler（/v1/* /status /healthz）
├── session/     # 会话粘性
├── panel/       # Web 面板（index.html + app.js go:embed）
├── scheduler/   # 定时任务（签到/旅行/保活/夜猫子）
├── prompt/      # 系统提示词体系
└── usage/reqlog/livecfg/logfmt/redisstore/  # 观测与支撑
```

## 执行纪律

1. **分批编码**：一次 ≤40% 总量 → 编译/测试 → 下一批；批批验证
2. **红线 15 条零违反**（尤其 R3 esc 转义、R8 报文不动、R13 前端重编译、R14 Go 1.27.2、R15 备份）
3. **禁顺手重构**：只动需求涉及的代码
4. **依赖方向**：`internal/*` 无循环依赖；只有 `upstream` 出站、只有 `auth` 持凭证
5. **收尾**：更新 `.harness/changes/{feat}/summary.md`

## 前端编码（internal/panel/）

- 资源 go:embed，改 JS/HTML 后**必须重编译**（R13）
- CSP `default-src 'none'`：禁内联脚本/事件处理器
- 所有用户输入渲染前过 `esc()`（转义 `& < > " '` 五字符）
- 新增渲染函数 → 配"有牙齿"的结构测试（node vm 切片）

## Token 约束

上下文 ≤4 份；已读不重复读；frontmatter 描述命中才读全文。

## 自检

- [ ] `go build ./...` 0 error
- [ ] 红线 15 条零违反
- [ ] 40% 分批且批批验证
- [ ] 前置条件满足（summary 存在）
- [ ] 前端改动已重编译（若涉及）

## 下一步

进入 → `unit-test-write`（单测），随后 `expert-reviewer`（评审）。
