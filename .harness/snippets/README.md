# .harness/snippets/ — 可复用代码块库

> 与 `.harness/prompts/`（提示词模板）分工：prompts 管"怎么干活"，snippets 管"这段代码该抄哪份"。
> 每块都必须有**真实出处**（file:line）和**判据**（什么时候用、什么时候别用）。

## 索引

| 代码块 | 什么时候用 | 唯一来源 |
|---|---|---|
| [jsonsafe-取值.md](jsonsafe-取值.md) | 从 `map[string]any` 取字段 | `internal/pkg/jsonsafe` |
| [timex-等待与日期.md](timex-等待与日期.md) | 可取消等待、本地日期/小时键 | `internal/pkg/timex` |
| [r8-协议常量.md](r8-协议常量.md) | 任何出站报文的域名/UA/Origin | `internal/upstream/protocol.go` |
| [json-响应写出.md](json-响应写出.md) | HTTP 端点回 JSON | 各包 `writeJSON`（**故意不合并**，见文内） |

## 维护纪律（三条）

1. **先有第二份，才建抽象**。只有一处调用的"通用函数"是负债：它多一层跳转，却没消掉任何重复。
   本仓已按此拒掉 3 个候选（`httpx` / `errx` / 全量 jsonsafe 迁移），证据见
   `docs/prd/REQ-yg-gateway-code-reuse-2026-11-02.md`。
2. **同名不等于重复**。`upstream.ExtractClientIP` 与 `server.extractClientIP` 名字几乎一样，
   语义是**故意不同**的（前者无条件取首段，后者按受信任代理判定）；合并会抹掉一条安全边界。
   判据：**读实现**，不看名字。
3. **新块必须带测试牙齿**。写进这里却没测试的"标准写法"，下一个人抄错也没人知道。
