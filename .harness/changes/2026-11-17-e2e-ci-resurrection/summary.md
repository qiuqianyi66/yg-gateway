# 2026-11-17 e2e CI 复活（此前从未真正跑起来）

## 问题

`e2e` workflow 自建立起**每一次都是红的**，但失败发生在**构建测试实例**这一步，
不是测试本身：

```
Join-Path: /home/runner/work/yg-gateway/yg-gateway/e2e/scripts/build-server.ps1:3
Line |
   3 |  $root = Join-Path $env:TEMP 'yg-e2e'
     |                    ~~~~~~~~~
     | Cannot bind argument to parameter 'Path' because it is null.
```

`$env:TEMP` 在 Linux runner 上不存在（Linux 用 `TMPDIR`）。**所以 e2e 从没跑过一条用例**——
「e2e 通过」这个说法在此之前没有证据支撑；反过来，「e2e 失败」也不代表产品有问题，
它只是脚本在 Windows 上写的。

## 四处跨平台不兼容（都已修）

| 位置 | 问题 | 修法 |
|---|---|---|
| `build-server.ps1:3` | `$env:TEMP` 为 null | `if ($env:TEMP) { $env:TEMP } else { [IO.Path]::GetTempPath() }` |
| `build-server.ps1:7` | 硬编码 `.exe` 后缀 | 按 `$env:OS` 选（Linux 无后缀） |
| `e2e-server.ps1:5-6` | 同上两处 | 同上 |
| `e2e-server.ps1:16` | `Get-NetTCPConnection` 是 Windows 专属 cmdlet | 只在 Windows 分支清理残留（Linux runner 无残留） |
| `e2e-server.ps1:25` | `'data\state.json'` 的反斜杠在 Linux 是文件名字符 | `Join-Path (Join-Path $root 'data') 'state.json'` |

## 修完之后暴露的真问题

runner 修好后 e2e 立刻从「构建失败」变成 **3 passed / 1 failed**，
失败的是 `smoke.spec.ts` 的 opsagent 用例：

```
await expect(page.locator('#opsagentBox')).toBeVisible();
```

`#opsagentBox` 在 `index.html` 里**根本不存在**——它是早期设计里「挂在账号池视图底部的卡片」，
实际实现改成了第 8 个独立视图 `#view-opsagent`。所以这是一个**测试断言错对象**的遗留 bug，
被「e2e 从来没跑起来」掩盖了很久。`internal/panel/opsagent.js` 顶部注释同样写的旧挂载点，一并纠正。

测试改为先点 `.nav a[data-view="opsagent"]` 切视图，再断言 `#view-opsagent` 可见。

## 验证

| 载体 | 结果 |
|---|---|
| 本地 `e2e/scripts/build-server.ps1`（Windows 分支） | exit 0，`built: ...\yg-e2e\yg-gateway-e2e.exe` |
| CI e2e run `44041176089`（修复前的提交 `5cdd73f`） | failure：构建阶段 exit 1（即上面那段 Join-Path 报错） |
| CI e2e run `44041444517`（修好 runner 的提交 `6bbaa0b`） | 跑起来了：3 passed / 1 failed（失败即上面那个断言错对象） |
| CI e2e run `44042384407`（改完断言的提交 `b5c2b56`） | **success**（4 条用例全过，3.5 分钟） |

**未验证**：Linux 分支的 `[IO.Path]::GetTempPath()` 与 `$env:OS` 判断只在 CI 里跑过
（就是上面那次 success），本地无法复现 Linux 环境。

## 怎么回滚

三个脚本文件与一个测试文件独立，`git revert` 即可。回滚后 e2e 会重新变红（但不是产品问题）。

## 顺带记一笔：release 的 build-image

`release` run `44042384432` 仍然是 failure，但**只有 `build-image` 一个 job**：

```
ERROR: failed to build: failed to solve: failed to push ghcr.io/qiuqianyi66/yg-gateway:main:
denied: permission_denied: write_package
```

证据链见 `2026-11-17-release-2.2.0/summary.md`：包最后更新停在 v2.0.0 那天、最近 8 次
release 全红、workflow permissions 已是 write——实为仓库改名后包与仓库的 Actions access
关联失效。用户级包没有 REST 授权端点，只能网页 `Package settings → Manage Actions access`
给 `qiuqianyi66/yg-gateway` 加 Write。**这不是本轮改动引入的**。
