# 回滚路线 — 2026-10-28 key-rpm-visualization-f6 (F6)

## 单提交回滚

本变更全部内容随一个 commit 提交（代码 + 文档），回滚：

```powershell
git -C "C:\Users\yanha\Desktop\yg-gateway" revert --no-edit <F6 commit hash>
```

## 回滚后状态

- keyRPMGuard 回到旧 allow 自增语义（只计数被放行的、有 rpm 配置的请求）。
- overview 不再输出 key_rpm（未接线），面板 Key 卡片无实况槽。
- 无配置字段需还原（本变更未引入任何 config 项）；无生产数据文件被改动。

## 注意

- 本变更不触碰上游协议（R8 无关）、不改 wire 形状（仅新增可省略字段）、无凭证路径
  （R1 无关）。回滚风险为低。
- 若回滚前生产面板已展示 RPM 实况（依赖新后端），回滚后槽位消失属预期（未观测=未知，
  非错误态），无需额外处理。
