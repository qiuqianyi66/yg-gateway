# 2026-10-24 仓库卫生 + 公网上传安全复核

## 改了什么

1. `.gitignore` 新增 4 条排除（均为**本机产物**，非项目产物）：
   - `/.workbuddy/` —— 本地 AI 代理记忆，含个人上下文与本机路径
   - `/yg-gateway-worktrees/` —— git worktree 元数据锚点
   - `/draft_*_folder/` —— 文档/图片工具的临时中间产物
   - `/docs/research/_shots/` —— 4.4MB 渲染截图导出物，其中 `report.json` 含本机绝对路径
2. 归档 `docs/research/YG-Gateway深度研究与产品PRD-v2.html`（40KB 源文档，无外链、无内嵌图片）。

## 为什么

仓库是 **PUBLIC**。上传前必须确认「本机/个人产物」不会被 `git add -A` 顺手带上公网。
`.workbuddy/` 这类目录不在原有忽略表里，属于**新增的风险面**（原有忽略表只覆盖了
`auths/`、`data/`、`config.json`、`*.key/.pem/.env`、二进制与备份）。

## 上传前安全复核结论（实测）

| 检查 | 方法 | 结论 |
|---|---|---|
| 是否有未推送提交 | `git rev-list --left-right --count origin/main...main` → `0 0`；8 个本地分支 `merge-base --is-ancestor` 全为真 | 无。代码与 origin/main 完全一致 |
| 全历史高危凭证 | `git grep -E 'sk-…|cb_…|ghp_…|AKIA…|eyJ…|BEGIN … PRIVATE KEY'` 遍历 `git rev-list --all` 296 个提交 | 仅 1 个唯一匹配 `cb_y5Dy46tPQGGWtueMxXbe`，是「企鹅教师助手」载体应用 **buddyId**（`internal/upstream/desktop.go:230` 注释），**不是凭证** |
| 敏感路径是否曾入库 | `git log --all --diff-filter=A --name-only` 过滤 `auths/ data/ backups/ verify/ config.json *.env *.key *.pem` | 从未入库 |
| 待上传文件内容 | 对 4 个未跟踪目录逐文件正则扫（`sk-`/`skey`/`uin=`/`Cookie:`/`Bearer`/JWT/私钥/绝对路径） | 无凭证；HTML 中 `Bearer` 两处均为**鉴权设计描述文字** |
| CI 密钥引用 | `.github/workflows/*.yml` 中 `secrets.` | 仅 `secrets.GITHUB_TOKEN` |

## 怎么验证

```powershell
git check-ignore -v docs/research/_shots/report.json .workbuddy/memory/2026-10-23.md `
  yg-gateway-worktrees/usage-incremental/.cwd-anchor draft_912a7f80_folder/draft.xml
git status --short --untracked-files=all      # 期望只剩 docs/research/*.html
powershell -NoProfile -File scripts/pre-commit.ps1 -Fast
```

## 怎么回滚

```powershell
git revert <本次 commit>        # 只回滚 .gitignore 段与 docs 归档，无代码影响
```

本地若要恢复旧行为：删除 `.gitignore` 末尾「本机代理产物 / 个人草稿」整段即可。

## 边界（明说的局限）

- 凭证扫描是**正则签名**匹配，不是语义审计：**未命中不等于绝无凭证**，只能说明
  「已知形态的高危 token 未出现」。若曾把真实 token 写进过非标准形态的字符串，
  本扫描覆盖不到。
- 历史已推送到公网：本次仅做**新增面**防漏，不重写历史（强推 rewrite 会让
  fork/镜像上的旧对象继续可访问，收益低风险高）。
