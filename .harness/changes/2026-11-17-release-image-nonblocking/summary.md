# build-image 不再拖红整个 release

日期：2026-11-17
类型：CI 策略调整（不改产物、不改发布逻辑）

## 一、事实

`build-image`（多架构镜像推 ghcr.io）自 v2.1.0 起持续失败：

```
denied: permission_denied: write_package
```

镜像最后一次成功推送是 **2026-10-24T08:49:39Z**（`gh api user/packages/container/yg-gateway/versions`
最新两条 version 的 created_at）。此后每一次 release run 都是红。

## 二、权限确实无解（本轮再确认一次）

| 探测 | 结果 |
|---|---|
| `gh api user/packages/container/yg-gateway` | 200，包存在（public，378 个版本） |
| `gh api users/qiuqianyi66/packages/container/yg-gateway/access` | **404 Not Found** |

GitHub 只对 **organization** 包提供 `/orgs/{org}/packages/.../access` 端点；
**用户级包没有对应 REST 端点**，只能在网页
`Package settings → Manage Actions access → Add repository → Write` 里授权。

## 三、为什么改

`publish`（发 Release）的依赖是 `needs: build`，**不依赖 build-image** ——
所以镜像失败从来没有阻塞二进制发布。它唯一的副作用是**把整个 release workflow 标红**。

这个红持续了三周，代价是：**release 这条信号线失效**。以后任何真回归
（编译失败、测试红、资产缺失）都会被同一个红淹没，没人能靠颜色区分。

## 四、改了什么

`.github/workflows/release.yml` 的 `build-image` job 加一行：

```yaml
continue-on-error: true
```

效果：

- 镜像失败时该 job 仍显示为失败（job 级 warning 图标），**不会静默**
- workflow 整体结论不再因此变红
- 权限修好后删除这一行即可恢复强约束

## 五、验证

| 项 | 方式 | 状态 |
|---|---|---|
| YAML 结构与缩进 | `Select-String` 逐行核对（174-177 行 job 属性同为 4 空格）+ 本地 Read | 已验 |
| 字段语义 | `continue-on-error` 是 GitHub Actions **job 级**合法键 | 已验（语法层面） |
| 实际行为 | 需下一次 push 后由 CI 解析确认 | **未验**（本地无法执行 Actions） |

## 六、未做的事（明确记录）

- **没有**把 `IMAGE_NAME` 换成新包名（新包由 Actions 首次创建通常自动获得 Write）。
  原因：会改变对外镜像地址、断开旧包 378 个版本的历史，属于产品决策，需用户拍板。
- **没有**动 `publish` 的依赖关系（它本来就不依赖 build-image，无需改）。

## 七、回滚

删除 `continue-on-error: true` 这一行即可。
