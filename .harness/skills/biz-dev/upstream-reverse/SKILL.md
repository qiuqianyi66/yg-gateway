---
name: upstream-reverse
description: 上游协议逆向与出站报文改造——改 CodeBuddy 上游（copilot.tencent.com）的请求/响应形态时必读，R8 红线约束（协议数据不得改名）。
---

# 上游协议逆向（YG Gateway）

## 触发条件

- 改出站报文（payload.go / headers.go / desktop.go 等）
- 新增上游端点调用
- 排查"上游 403/400/风控"类问题

## 铁律：R8 红线

**发给上游的协议数据不得随意改名**，包括但不限于：
- `wb2api-<ms>` 形式的 conversationId
- `chatcmpl-wb2api` 哨兵 id
- UA 指纹（`WorkBuddy/<ver> ... CLI/<ver>` 三段式）
- `X-IDE-*` / `X-Product` / `X-Device-Token` 归因头

改名有风控风险（上游按指纹识别客户端形态）。要改必须先评估对风控判据的影响，并留回滚。

## 出站架构（单一事实来源）

- **唯一出站口**：`internal/upstream/client.go` 的 `HTTP` / `ChatHTTP`（共享同一 Transport）
- **base 硬编码**：CN `copilot.tencent.com` / billing `codebuddy.cn` / web `workbuddy.cn`；global 默认 `workbuddy.ai`
- **路径常量拼接**：所有路径写死，唯一用户可控片段 `taskCode` 已 `url.PathEscape`
- **改写管线**：`payload.go` 集中改 body（强制 stream / 角色归一 / 思维链注入 / 指纹脱敏）

## 改出站报文的流程

1. 先逆向：用 scripts/ 下的诊断脚本（probe_active.py / probe_max_tokens.py）抓真实报文
2. 定位改写点：payload.go 的对应函数
3. 改后必须：`go test ./internal/upstream/` 全绿 + 真实账号端到端冒烟（httptest mock 只能测结构，不能测风控）
4. 记录：`.harness/changes/{feat}/summary.md` 写明改了什么报文、为什么、回滚方式

## 安全边界（防 SSRF）

- base 硬编码 + 路径常量 + PathEscape → 无用户可控出站 host，SSRF 面不存在
- 若未来要加"可配置 base"，必须加**白名单校验**（域名/IP 段），禁止裸接受用户输入

## 自检

- [ ] R8 红线未违反（协议数据未改名）
- [ ] 出站改动只在 upstream 包内，无他包直连上游
- [ ] `go test ./internal/upstream/` 全绿
- [ ] 真实账号端到端冒烟通过

## 下一步

改动完成 → `expert-reviewer`（安全维度重点审 SSRF/凭证）。
