# REQ-noauth-wildcard-listen-2026-11-11

空密钥 + 非回环监听 = 裸暴露，改为启动期拒绝。

## 一、背景（来自实测定性，不是推测）

对 `e07a6c8` 只读核查后确认两条同时成立于同一配置的行为：

1. 空密钥 = 不鉴权。`httpauth.Authenticate` 在 index 为 nil 且 singleKey 为空时返回放行
   （internal/httpauth/index.go:190-192）；面板同理，`panel.authenticateAdmin` 在无多 Key
   且 api_key 空时直接放行（internal/panel/panel.go:198-202）。
2. 默认监听是全接口。`Default()` 的 Listen 为 ":7863"（cmd/server/config.go:507）。

两者叠加 = /v1/* 与 /panel/api/* 对同网段匿名开放。当前唯一的提示是 WriteDefault 的
一条注释（config.go:666），没有启动期判据——实测 main.go 启动装配段（:495-580）不含
任何「空密钥 + 非回环 → 拒绝」检查。
## 二、范围

- 只拦「没有任何生效密钥」且「listen 非回环」这一种组合。
- 显式绑回环（127.0.0.1 / ::1 / localhost）允许空密钥运行：本机调试的合法用法，
  攻击面只剩本机进程，不一起打死。
- 不涉及凭据静态加密（会与插件共读 auths/*.json 冲突，需单独决策）。

## 三、验收（Given-When-Then）

- G1：Given listen=":7863" 且 api_key、api_keys 均为空 / When 启动 / Then 退出非 0，
  日志含「拒绝启动」、当前 listen 与三条解法。
- G2：Given listen 为 "0.0.0.0:7863" 或 "[::]:7863" 且无密钥 / Then 同 G1。
- G3：Given listen="192.168.1.10:7863"（显式非回环）且无密钥 / Then 同 G1。
- G4：Given listen="127.0.0.1:7863" 且无密钥 / Then 正常启动（放行）。
- G5：Given 存在生效密钥（api_key 非空，或 api_keys 有未禁用且 key 非空条目）
  / Then 任意监听都放行——判据与 buildAuthSnapshot 同口径。
- G6：Given api_keys 条目全部 enabled:false / Then 视同无密钥（拒绝）。
- G7：Given 注入「authConfigured 恒返回 true」的变异 / Then G1 用例必须 FAIL。
## 四、实现

- 新增 cmd/server/config_exposure.go：classifyListen（回环 / 全网卡 / 具体地址）、
  authConfigured（与运行时索引同口径）、CheckUnauthenticatedExposure。
- cmd/server/main.go：Load 成功后、账号装配前调用，log.Fatalf 退出。
- 单测 cmd/server/config_exposure_test.go 覆盖 G1-G6。
## 五、不做（明确排除）

- 不做静态加密（与插件共读 auth 文件冲突，需单独 ADR）。

- 不动默认监听值，由启动闸兜底（避免影响现有 Docker 端口映射）。

- 不做「告警后继续启动」：该组合不是配置漂移，是暴露面，必须拒绝。

- G8：Given 实例以全网卡监听运行、面板提交清空 api_key / When 保存 / Then 返回错误且**不落盘、不热应用**（同一判据在 saveConfig 复检）。

- G9：Given security.allow_unauthenticated=true / When 无密钥 + 全网卡启动 / Then 放行，且每次启动必打 WARN（默认 false 时仍拒绝）。
