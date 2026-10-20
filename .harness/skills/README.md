# 技能路由表（静态）

> 为什么是**静态路由表**而不是让 AI 自己检索：Vercel evals 实测——把指令压成短静态路由表
> pass rate 100%，动态技能检索 79%。代价是这张表必须**与磁盘一致**，所以
> `scripts/check-harness.ps1` 检项 [3] 双向比对：
> - 磁盘有、表上没列 → **视为不存在**（模型不会用，等于白写）→ 报错；
> - 表上列了、磁盘没有 → 断链 → 报错。
>
> 用法：先看「跳过」列决定用不用，命中再读正文 `.harness/skills/<族>/<名>/SKILL.md`。
> 增删技能必须**同步这张表**，否则门禁红（这是故意的：把漂移变成红灯）。

| 技能 | 干什么 | 什么时候跳过 |
|---|---|---|
| `main-dev/request-analysis` | 需求 → F 编号 + Given-When-Then + 影响分析 | 需求已精确到「改哪个文件的哪一行」 |
| `main-dev/coding-skill` | Go 分层编码 + 内嵌面板前端，红线 15 条 | 纯文档/注释改动；只跑构建不写代码 |
| `main-dev/unit-test-write` | Go 表驱动 + 前端 node/vm 切片测试（测试必须有牙齿） | 纯样式/文案改动无逻辑分支；写 fixture |
| `main-dev/unit-test-ci` | CI 门禁验证：build/vet/test + govulncheck + node --check | 只改文档/注释；调查性阅读 |
| `main-dev/expert-reviewer` | 6 维评审，问题按 🔴🟡🟢🔵 分级，🟡 清零才过 | 纯 gofmt 格式漂移；实验分支未准备合入 |
| `main-dev/deploy-verify` | 换版：二进制 swap + 备份（R15）+ healthz 冒烟 | 纯本地实验；只改配置不换二进制 |
| `biz-dev/panel-frontend` | 面板前端（go:embed 单文件 + 严格 CSP + esc() 转义） | 纯后端改动且不动 app.js/index.html |
| `biz-dev/upstream-reverse` | 上游协议逆向与出站报文改造（R8 不得改名） | 不改出站报文；只调超时/重试参数 |
| `ops/credential-acl` | 凭证落盘权限（Windows icacls + Go 0600，R1/R7） | 不涉及 auths/ 与凭证落盘的改动 |
| `trouble-shooting/bug-diagnose` | 难 bug 总入口：先建 tight feedback loop 再按 6 阶段排查 | 已知根因且范围明确；已确认是上游问题 |
| `trouble-shooting/upstream-fault` | 上游故障定位（WAF 403 / 风控 / 半死连接 / SSE 断流） | 问题在网关内部；上游偶发失败已自动轮转 |

## 怎么加一个新技能

1. 建 `.harness/skills/<族>/<名>/SKILL.md`，frontmatter 必须有 `name` + `description`，
   且 `name` **等于目录名**（检项 [2] 判这个，不一致即红）；
2. **在上方表格加一行**（含 `<族>/<名>` 的反引号路径）——漏了会被检项 [3] 判「磁盘有但表上未列」；
3. `description` 里写清【触发】与【跳过】：跳过条件比触发条件更容易被忽略，
   但它决定这个技能会不会被误用（误用 = 拿流程去套不该套的改动）。
