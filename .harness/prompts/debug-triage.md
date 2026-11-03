# 模板：问题诊断（复现 → 定位 → 归因 → 修复）

> 用法：整段粘贴，替换 `<...>`。适用于"行为不对但还不知道在哪"的场景。
> 已经确定是打上游出问题 → 转 `.harness/skills/trouble-shooting/`（upstream-fault）。

---

现象：**<一句话描述>**。已知信息：**<时间 / 端口 / 账号 / 请求样例 / 报错原文>**。

## 第 0 段：先建反馈回路（没有它，后面全是猜）

在动手读代码之前，先拿到一条**能稳定变红**的命令：

- 单测级：写一个 FAIL 的测试（最小输入，还原现象）
- 接口级：一条 `curl` / 脚本，每次都复现同一现象
- 日志级：一段可重放的请求 + 对应日志行

做不到稳定复现 → 明确写 **unknown + requires**（还需要什么证据），
**不许**先猜原因再去找证据（L-22）。

## 第 1 段：复现（observation，不是 inference）

- 记录**完整**输入与**完整**输出（原文，不转述）
- 标注四态：哪部分是直接观测（HTTP 状态码 / 日志行 / 上游 body 原文），
  哪部分是推测（"大概是风控"这类）
- 手动回归优先用仓库脚本，别手敲重定向：
  `python scripts/yg-regression-test.py`（`--skip S4,S8` 跳过危险用例）

## 第 2 段：定位（二分，不靠读全仓）

按证据强度从高到低排：

1. **日志/指标**：`internal/reqlog` 归档、`/panel/api/*`、进程 stderr
2. **机器能跑的**：`--help` / `-Detail` / 直接调用一次 > 读三处代码（L-15 第 3 条）
3. **代码**：从现象的关键字符串反查（`grep` 原始输出要留档），沿调用链向内走

分诊表（先判层级，别在南辕北辙的层里翻）：

| 现象 | 先看哪 |
|---|---|
| 4xx/5xx 来自上游 | `internal/upstream/{errors,client,transport}.go` + 上游 body 原文 |
| 选号/冷却异常 | `internal/pool/{pick,cooldown,state}.go` |
| 流式断/半截 | `internal/upstream/sse.go` + SSE 帧原文 |
| 面板数字不对 | `internal/panel/*` + `/status` 原始 JSON（先分清 observation/derivation） |
| 调度没跑 | `internal/scheduler/*` + 下一次唤醒时间计算 |

## 第 3 段：归因（写到"为什么"为止）

- 根因必须是**可验证的机制**，不是"可能是并发问题"
- 区分：直接原因 / 触发条件 / 为什么没被门禁拦住（这一条决定要不要加机器检查）
- 若根因是"缺失被读成否"这类语义错，按 AGENTS.md 四态纪律给出正确口径

## 第 4 段：修复

- 最小改动，定点编辑（L-10）；**不许顺手重构**
- 测试必须有牙齿：把修复回退，测试必须红
- 不许用 `_ =` 吞掉新出现的错误（R10）；不许用放宽阈值让棘轮变绿
- 修复 + 测试放**同一个提交**（L-11）

## 收尾

```powershell
go test ./...
powershell -NoProfile -File scripts/pre-commit.ps1
```

落 `.harness/changes/<feat>/summary.md`：现象 → 复现命令 → 根因（含证据行号）→
修复 → 为什么门禁没拦住（若适用，提一句要不要升级为规则/机器检查）。
**同一条坑第 2 次出现必须升级**（`.harness/LESSONS.md` Seen 计数制）。
