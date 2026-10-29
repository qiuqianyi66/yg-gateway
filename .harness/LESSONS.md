# LESSONS 索引（条目 + 一句话 + Seen）· 正文按需：`.harness/lessons/L-NN.md`

> **Seen 计数制**：第 2 次出现的坑必须升级为规则（`rules/`）或机器检查（`scripts/`），`Seen ≥ 2` 未升级由检项 [7] 判 error。
> 动手前先读本索引、命中再读正文；新增记 `Seen 1`；会伪造结论的坑才记，flake 见 [flake.md](lessons/flake.md)。

| 条目 | 一句话 | Seen | 状态 | 正文 |
|---|---|---|---|---|
| L-1 | PS 引号陷阱：反斜杠不是转义符；提交走 `-F` | 3 | 机器检查 #3 | [L-01](lessons/L-01.md) |
| L-2 | 变异必须在副本里做，并自证在副本 | 2 | 硬规则 | [L-02](lessons/L-02.md) |
| L-3 | 未加引号变量 → 仓库根字面名垃圾文件 | 2 | 机器检查 #4 | [L-03](lessons/L-03.md) |
| L-4 | 并行 agent 的结构化返回值要原样上返 | 1 | 规则 | [L-04](lessons/L-04.md) |
| L-5 | 「行数」必须先问口径（非空行 vs 实际行） | 1 | 口径声明 | [L-05](lessons/L-05.md) |
| L-6 | CI 里「本次改动」≠ 工作区 diff | 1 | pre-commit 参数 | [L-06](lessons/L-06.md) |
| L-7 | 反斜杠字面量路径在 Linux 上不是分隔符 | 1 | 规则（未机器化） | [L-07](lessons/L-07.md) |
| L-8 | 格式检查是 `gofmt -l`，不是 `go fmt -l` | 1 | AGENTS 门禁 | [L-08](lessons/L-08.md) |
| L-9 | 换二进制要等端口释放；别信 start.bat 重定向 | 1 | 脚本 deploy-prod | [L-09](lessons/L-09.md) |
| L-10 | 改文件用定点编辑，别做字符串手术 | 4 | 规则 CHECKLIST§十 | [L-10](lessons/L-10.md) |
| L-11 | 测试要与它依赖的生产改动同一个提交 | 1 | 规则 | [L-11](lessons/L-11.md) |
| L-12 | 非 PE 当 exe 启动会弹模态框卡死 launcher | 1 | 脚本 deploy-prod | [L-12](lessons/L-12.md) |
| L-13 | PS 5.1 IWR 非 2xx 时 body 已被读掉 | 1 | 脚本 deploy-prod | [L-13](lessons/L-13.md) |
| L-14 | 覆盖被占用的 exe 会失败，须改名替换 | 1 | 脚本 deploy-prod | [L-14](lessons/L-14.md) |
| L-15 | 否定性断言必须穷尽证据源 | 2 | 规则 AGENTS | [L-15](lessons/L-15.md) |
| L-16 | 自评审 PASS ≠ 通过；独立复审另起一人 | 1 | 规则 流程规范§四 | [L-16](lessons/L-16.md) |
| L-17 | 「变异全绿」先问是不是跑在副本里 | 1 | 机器检查 mutation | [L-17](lessons/L-17.md) |
| L-18 | 就绪判据要真实状态，不是「等一会儿」 | 1 | 规则 | [L-18](lessons/L-18.md) |
| L-19 | 聚合脚本要自证输入完好，失败时吐子输出 | 1 | 脚本 run-all | [L-19](lessons/L-19.md) |
| L-20 | 外部进程 stdout/stderr 要分开捕获 | 1 | 自检测试 panel | [L-20](lessons/L-20.md) |
| L-21 | 提交只含本任务产生的文件，外部落入工作树的文件不碰 | 1 | 规则（用户指令） | [L-21](lessons/L-21.md) |
| L-22 | 结论/方案前必须穷尽证据（只读片段断言全貌/未验证前提当事实） | 1 | 规则 AGENTS | [L-22](lessons/L-22.md) |
