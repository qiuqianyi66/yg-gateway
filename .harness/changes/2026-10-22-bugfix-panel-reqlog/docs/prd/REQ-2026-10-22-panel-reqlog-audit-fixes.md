# REQ-2026-10-22 全仓体检修复 C-3 / C-5 / C-6

> 来源：2026-10-22 版块 C（panel/metrics/reqlog/alerting/logfmt/prompt）只读体检，
> 三条 P2 修复。分级：L2（业务逻辑），无 ADR（无难逆转 / 无未来读者会困惑的权衡）。

## 必要性

1. **要解决的真实问题是什么？**
   - C-3：`ReadArchive` 的 From/To 时间过滤只在行内生效，面板「运行日志」每次查询要
     把整个归档目录（上限 100MB）逐行 parse 一遍——归档跑满时一次面板查询就是百 MB 级 CPU。
   - C-5：`truncateStr` 按字节切错误文本，切点落在中文多字节字符中间时出非法 UTF-8，
     经 json.Marshal 变 U+FFFD 乱码（runModelChat 的上游错误透传）。
   - C-6：`loginPoll` 的 `known` 检查在 loginMu 外，双 Tab / 手点刷新并发 poll 时
     两个请求双双通过检查，各自走完 SaveAtomic + Pool.Add + 签到（幂等无害，双倍打上游）。
2. **不做会怎样？** 三项均为 P2：功能不错、安全不破，但分别带来可观测的 CPU 浪费、
   前端一行乱码、偶发双倍上游调用。危害可观测但不阻断。
3. **现状是否已变？** 三项均经全仓 grep + 代码走查实测确认当前成立（审查报告 C-3/C-5/C-6）。
4. **更便宜的手段？** C-3 用文件名日期段做整文件粗筛（不改匹配逻辑，零行为差）；
   C-5 直接复用 `logfmt.Truncate`（rune 边界回退已是既有实现）；C-6 在既有 loginMu 内
   做 check-and-delete 原子占位（最小改动，保留 pending 轮询路径）。

结论：**做**（降级做：只修这三条 P2，其余体检发现项维持现状记录）。

## F-编号需求 + Given-When-Then

### F1（C-3）ReadArchive 文件级日期粗筛

- **Given** 归档目录含跨三个自然日的归档文件
- **When** `ReadArchive` 带 From/To 过滤
- **Then** 文件日期段 < From 日 或 > To 日 的整文件不读，结果集与逐文件全读完全一致
  （闭区间命中语义不变）。

### F2（C-5）truncateStr 回退 rune 边界

- **Given** 长中文错误文本
- **When** `truncateStr(s, 160)`
- **Then** 结果是合法 UTF-8（无 U+FFFD），长度 ≤ 160。

### F3（C-6）loginPoll check-and-delete 原子占位

- **Given** 同一 state 注册在 `p.logins`
- **When** 两个并发 `loginPoll` 同时到达且上游 token 端点可用
- **Then** 恰好一个走完落盘 + 签到（account 端点只被打一次），另一个 404。
  pending（token 未发）路径仍 `done:false` 继续轮询，不破坏既有错误路径。
