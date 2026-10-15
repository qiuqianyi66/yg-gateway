---
name: unit-test-ci
version: 1
description: CI 门禁验证——go build/vet/test + govulncheck + node --check 全绿才可进评审。【跳过】只改文档/注释（仍建议跑一次 build 以防万一）；调查性阅读代码不改动。
type: executable
verification: go build ./... && go vet ./... && go test ./...
---

# Unit Test CI（YG Gateway）

## 触发条件

- 阶段 5 完成后验证；提交前；构建失败排查。

## 工作流

1. **构建**：`go build ./...`（0 error）
2. **静态检查**：`go vet ./...`（0 输出）
3. **测试**：`go test ./...`（全绿；包数随 cmd/internal 演进会变，不写死）
4. **前端语法**：`node --check internal/panel/app.js`（app.js 是 go:embed，Go 编译器不查其内容，一次语法错误就让面板白屏而 Go 测试全绿——必须单独校验）
5. **漏洞扫描**（依赖/安全相关改动时）：`govulncheck ./...`（零告警；工具链必须 Go 1.27.2+，低版本带 9 个 stdlib CVE）
6. **XSS 探针**（面板改动时）：4 种 payload 注入 → foreign tags NONE / event attrs NONE → SAFE

## 验证命令速查（本机）

- Go 工具链：`$env:TEMP\go\bin\go.exe`（Go 1.27.2）
- GOPROXY：`https://goproxy.cn,direct`（proxy.golang.org 本机不可达）
- 构建产物：`CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o yg-gateway.exe ./cmd/server`

## 红线

- 全绿才可进阶段 6 评审；govulncheck 告警不清零不进评审。

## 自检

- [ ] go build / vet / test 全绿
- [ ] node --check 通过（前端改动时）
- [ ] govulncheck 零告警（依赖/安全改动时）
- [ ] XSS 探针 SAFE（面板改动时）

## 下一步

进入 → `expert-reviewer`（专家评审）。
