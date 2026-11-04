# REQ — yg-gateway 面板鉴权机制化 + 代理 client 去重（P1-3 / P1-2）

日期：2026-11-04
来源：`docs/PLAN-magpie-borrow-2026-11-04.md` P1 批
基线：`6bef0d2`

## 背景

magpie REA 的 P1 建议落地。面板鉴权经审计为 Bearer 模式（非 cookie），
cookie 组合拳前提不成立；真实缺口是「API 端点手工包 withAuth，新增漏包即裸奔」。
代理 client 构造 `clientFor`/`chatClientFor` 结构重复（代理构造 + 双检锁缓存）。

## 需求

### P1-3 面板 API 端点鉴权机制化

Given 面板 API 端点经 `p.api(method, path, handler)` 注册
When 无 Authorization 头访问该端点
Then 返回 401，且 handler 不被调用

Given 同一端点
When 携带管理员 api_key 的 Bearer 头访问
Then 鉴权通过并到达 handler

验收：`routes()` 中全部 `/panel/api/*` 端点改走 `api()`；新增端点默认强制鉴权
（机制保证，非约定）。

### P1-2 代理 client 构造去重

Given 账号配置非空 proxyURL
When `clientFor` 与 `chatClientFor` 分别首次调用
Then 各返回独立实例（cacheKey `chat:` 前缀隔离），proxyEff 登记一致
And 再次调用同函数命中缓存（不重复构造）

## 非目标（留档）

- P1-1 `min_request_interval` 默认值：待观测账号请求分布后校准，本次不设值。
- magpie 式 rpmTransport 滑动窗硬闸：P2 独立项，不做。
