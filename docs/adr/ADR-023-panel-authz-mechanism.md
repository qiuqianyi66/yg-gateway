# ADR-023：面板鉴权机制化——Bearer 审计推翻 cookie 假设，api() 强制

> 状态：**已实施**（P1-3，`b544037`）。
> 依据：REQ-yg-gateway-p1-panel-authz-dedup-2026-11-04 + magpie REA ④-5
> （Web 面板鉴权组合拳，observation/inference 混合）。
> 决策点：攻击面假设修正、机制化形态、静态资源边界。

## 背景

REA ④-5 建议「抄 magpie 面板鉴权组合拳」。规划先验假设：面板存在 cookie/HttpOnly/
BREACH 攻击面，需上 CSRF 防护等组合拳。**审计推翻了前提**（本次决策的核心证据）：

- 面板是 **Bearer 鉴权**（`withAuth` → `authenticateAdmin`，panel.go:252-307），
  只认单 api_key（Live 优先回落静态），不认多 Key；空 key 多 Key 模式 fail-closed；
  `httpauth.VerifyBearer` 常量时间比较（R5）。
- CSRF 天然免疫：跨站请求不携带自定义 Authorization 头（浏览器不会自动附加）。
- cookie 不存在 → HttpOnly/BREACH 攻击面不存在。

因此 cookie 组合拳**不引入**（先审计再抄：套用结论前先验证前提，L-22）。

## 候选对比

### D1：机制化形态

- 真实缺口是**机制性**的：42 个 API 端点全部手工 `mux.HandleFunc(..., p.withAuth(h))`，
  新增端点漏包 = 管理端点裸奔（可读写配置、禁用/移除账号、重置密钥）——约定式鉴权
  靠人自觉，漏一个不报错（L-10 同类：字符串手术式的逐个包裹）。
- 候选 A：继续手工包裹 + 补一条「新增端点必须 withAuth」约定。约定不可机器验证，
  漏包依旧静默。**否决**。
- 候选 B（采用）：**`p.api(method, path, h)` 私有注册方法，内部强制 `p.withAuth(h)`**；
  全部 42 个 API 端点改走 p.api。新增端点默认强制鉴权（机制保证，非约定）。
  牙齿测试：TestPanelAPIMethodRequiresAuth——api() 内部换回裸 h，第一条断言立即红。

### D2：静态资源边界

- 静态资源（前端壳/JS/CSS，21 条）**保留裸注册**——路径不在 `/panel/api/` 下，
  无敏感数据；统一强制鉴权会破坏「面板打开即见 UI、接口才要鉴权」的分层。
- 边界判据：`/panel/api/*` 端点一律 api()；其余路径保持现状。

## 合同

- `/panel/api/*` 全部经 p.api 注册；新增 API 端点必须走 p.api（漏走 = 裸奔 = 评审红线）。
- 鉴权语义不变（Bearer 单 key、Live 优先回落静态、常量时间比较 R5）。
- panel.go 超 1000 行触发 depth 门禁 → routes()/api()/ServeHTTP 拆至 routes.go
  （非 -Update 掩盖，oversizeFiles 保持 2）。

## 副作用与回滚

- 无配置、无持久状态变化；鉴权行为与回滚前逐端点等价。
- 回滚：git revert b544037（routes.go 删除、panel.go 恢复 42 行手工 withAuth，
  鉴权行为不变——本 ADR 的价值在「机制防漏包」，回滚即退回约定式）。
