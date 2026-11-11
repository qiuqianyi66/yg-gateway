# refuse-noauth-wildcard 变更记录

需求：docs/prd/REQ-noauth-wildcard-listen-2026-11-11.md

## 一、改了什么

- 新增 cmd/server/config_exposure.go：listen 暴露面分类、生效密钥判定、启动闸。
- cmd/server/main.go：Load 成功后、账号装配前调用启动闸，失败即 log.Fatalf。
- 新增 cmd/server/config_exposure_test.go：10 个分类用例 + 7 个闸门用例（含全部 disabled 与 nil 配置）。

## 二、怎么验证的

- gofmt -l ./cmd ./internal：无输出。
- go test ./cmd/server -run 'ClassifyListen|CheckUnauthenticatedExposure'：全 PASS。
- 全量 gate：scripts/pre-commit.ps1（结果见本次交付说明）。
- 生产配置为 listen=127.0.0.1:7863 且 api_key 已配，启动闸判为放行，与部署前行为一致。

## 三、怎么回滚

- 代码：git revert 本次提交即可，无状态迁移、无配置键新增。
- 生产：scripts/deploy-prod.ps1 每次换版都留 backup_path 与 sha256，退出码 2 表示已自动回滚。

## 四、边界与未知

- 凭据静态加密未做（与插件共读 auth 文件冲突，需单独决策）。
- 主机名形式的 listen 从严按非回环处理，宁可多拒一次。

## 五、热更新路径补漏

- 复核发现：api_key 属热生效字段，面板清空密钥会立即生效，冷启动闸不覆盖 → 实例在全网卡监听时会当场变成无鉴权。
- 处置：saveConfig 在 ParseConfig 之后用同一判据复检，失败不落盘不热应用；单测 TestSaveConfigRejectsClearingKeyOnWildcard 锁死（含「磁盘原密钥未被改写」断言）。

## 六、显式开关（替代硬墙的一刀切）

- 新增 security.allow_unauthenticated（默认 false）：把「我知道自己在裸奔」落成可审计配置，而不是逼人降级版本。
- 开启且无密钥时，启动日志每次打 WARN；config.example.json 补该键（false）。
- 单测 TestCheckExposureAllowUnauthenticatedOptIn 锁死「默认拒绝 / 显式放行」。

## 七、拆 config.go（解门禁压线）

- Default() 与 defaultPricing() 原样迁到 cmd/server/config_defaults.go，config.go 892 行，留出后续改动余量；纯搬移，行为不变（go test ./... 全绿）。
