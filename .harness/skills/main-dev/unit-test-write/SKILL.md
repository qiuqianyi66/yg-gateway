---
name: unit-test-write
description: 单元测试编写——Go 表驱动测试 + 前端 node/vm 切片测试，测试必须有牙齿（还原 bug 必须 FAIL）。对应十阶段流水线阶段 5。
---

# 单元测试编写（YG Gateway）

## 触发条件

- 阶段 5 单元测试；新增功能/修复 bug 后补测试。

## 测试形态（本项目两类）

### 1. Go 表驱动测试（`*_test.go`）

- 表驱动为主（见 `internal/pool` 各 `_test.go` 范式）
- 涉及上游协议用 `httptest.Server` mock，禁打真实上游
- 涉及 handler 用 `httptest.NewRecorder` + `NewHandler(Config{...})`

### 2. 前端切片测试（`internal/panel/frontend_test.go`）

app.js 是单文件无模块，测试用 `node` + `vm` 执行**代码切片**：

```go
const start = src.indexOf('function expBatches')
const end = src.indexOf('async function loadExpiry')
vm.runInContext(src.slice(start, end) + '\nthis.renderExpiry = renderExpiry;', ctx)
```

## 切片契约（铁律，违反必踩坑）

1. **函数外的常量不进切片**：测试用 `indexOf` 定位，提到函数外的常量会 ReferenceError。
   共用常量若无法同时落进两个切片，就**保留重复**（见 panel-spec 教训）。
2. **注释里禁写函数声明字面量**：`indexOf` 会先命中注释而非函数本身，切片被从中间切断
   （`SyntaxError: Unexpected end of input`）。注释用描述性表述。
3. **日期用本地时区构造**：app.js 用 `new Date(dateStr+'T00:00:00')` 本地解析，
   探针用 `toISOString()`（UTC）会在 UTC+8 下退一天，边界用例假失败。

## 牙齿要求（强制）

- 每个修复类测试，验证「还原 bug → FAIL」一次。不验证牙齿的测试等于没写。
- 边界用例显式覆盖：危险度边界（3/4、7/8）、`days=0` 不除零、转义引号闭合等。

## 自检

- [ ] `go test ./...` 全绿
- [ ] 每个修复有对应测试且验过牙齿
- [ ] 前端切片遵守契约（无模块级常量、无函数字面量注释、本地日期）

## 下一步

进入 → `unit-test-ci`（CI 门禁）。
