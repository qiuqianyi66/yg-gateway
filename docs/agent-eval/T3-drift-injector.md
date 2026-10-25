# T3 漂移注入件（评分人专用，勿发给被测 agent）

> 用途：金样任务 T3（文档漂移狩猎）的标准化注入。重放前由评分人在**独立分支或
> 提交前的工作区**手工执行以下 3 处编辑并 commit，任务完成后 revert。注入即记录，
> 不得临时加题。

## 注入清单（3 处，均有可实测的真相）

| # | 文件 | 注入编辑 | 真相（评分答案） | 实测命令 |
|---|---|---|---|---|
| D1 | `README.md` 多 API Key 表 | 把 Key 表中的字段名 `allowed_cidrs` 改成 `allowed_ips` | 字段名是 `allowed_cidrs`（ADR-013 Key 级 IP 白名单） | `git grep allowed_cidrs -- internal/ cmd/` |
| D2 | `AGENTS.md` 门禁行 | 「23 包：21 ok + 2 no test files」改成「24 包：22 ok + 2 no test files」 | `go list ./...` = 23 个包；`go test ./...` = 21 ok + 2 no test files | `go list ./... \| wc -l`（用 YG_GO_EXE 工具链） |
| D3 | `.harness/rules/工程结构.md` panel 清单 | 删除 `opsagent_view.go` 一行 | 该文件存在且被 `/panel/api/opsagent/status` 使用 | `ls internal/panel/opsagent_view.go` |

## 评分口径（对应 GOLDEN-TASKS T3）

- 3/3 找到并修正 = 2 分；漏 1 处 = 1 分；漏 ≥2 或引入新漂移 = 0 分。
- 加分观察（不影响 0-2 分档）：被测 agent 是否**先实测再改**（引用上面任一命令的
  输出），还是凭"感觉不对"直接编辑。
- 常驻层预算必须保持 ≤15822：被测 agent 修 D2/D3 时若超预算，按其处置方式计分
  （主动等价压缩=正确；无视=瑕疵）。

## 回滚

任务结束：`git revert <注入提交>`；注入提交须与被测 agent 的产出提交**分开**，
避免污染验收记录。
