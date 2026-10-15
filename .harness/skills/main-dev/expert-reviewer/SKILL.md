---
name: expert-reviewer
version: 1
description: 代码评审——6 维评审（正确性/性能/安全/一致性/可维护性/架构合规），问题按 🔴🟡🟢🔵 分级，🟡 清零才过。【跳过】纯格式漂移（gofmt 结果）且无逻辑变化；单人实验分支尚未准备合入。注意：即使跳过完整评审，红线（R1-R15）自检**不可省**。
---

# Expert Reviewer（YG Gateway）

## 触发条件

- 阶段 6 代码评审；提交前自查。

## 工作流

1. 读变更清单（git diff + `.harness/changes/{feat}/summary.md`），覆盖**全部变更文件**。
2. 6 维评审：
   - **正确性**：逻辑 / 边界 / 状态流转 / 并发（锁、goroutine 回收）
   - **性能**：连接池 / 上游请求量 / 锁竞争 / goroutine 泄漏 / 内存（超大 body）
   - **安全**：按下表逐项扫描
   - **一致性**：与 wiki/glossary 术语、与既有代码风格
   - **可维护性**：命名 / 抽象 / 重复 / 复杂度 / 注释讲 why
   - **架构合规**：cmd/internal 分层、依赖方向、红线 15 条
3. 分级输出：🔴 阻断 / 🟡 主要（清零才过）/ 🟢 次要（记录）/ 🔵 提示（不阻塞）。
4. 每条 finding 附 `FIX: <最短解法>` + `See: <红线编号/文档路径>`。
5. 收尾：评审记录落盘 `.harness/changes/{feat}/review.md`；闭环 ≤2 轮。

## 安全维度（本项目特有 + OWASP）

**本项目特有的高优项**：
- **凭证泄露**（R1）：token 禁入日志/state.json/归档 JSONL；日志脱敏（UID 前 8 位）
- **上游报文形态**（R8）：conversationId / 哨兵 id / UA 指纹不得改名
- **伪造 XFF 透传**：受信任代理闸门是否绕过（`security.trusted_proxy`）
- **入站 body 上限**（F-001）：新读 body 处是否漏加 MaxBytesReader
- **面板 XSS**（R3）：新渲染函数是否漏 esc()、是否有裸 innerHTML
- **路径穿越**（R4）：UID 拼文件路径是否白名单校验

**OWASP 通用**：注入 / XSS / 越权（IDOR）/ 敏感数据暴露 / SSRF / 反序列化 / 日志注入 / 依赖 CVE。

## 红线

- 🔴 禁止降级；🟡 未清零不过评审；覆盖全部变更文件。

## 自检

- [ ] 6 维全覆盖
- [ ] 🔴 零残留、🟡 清零
- [ ] 每条 finding 含 FIX 与规则出处（🔴 必须）
- [ ] 评审记录已落盘

## 下一步

评审通过 → 提交 → `deploy-verify`（部署验证）。
