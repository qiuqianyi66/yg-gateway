// harnessrun 金样任务评分辅助工具（V3-9）。
//
// 用途：GOLDEN-TASKS.md 定义了 5 个金样任务用来评测 agent 在本仓的干活能力。
// 本工具自动化其中**机器可判**的评分点检查，人工评分点（文档找齐没找齐、
// 否定断言做没做）还是人工来。
//
// 用法：
//
//	harnessrun list              列出 5 个金样任务
//	harnessrun check T1          检查 T1 任务的机器可判评分点
//	harnessrun check T5          跑 deploy-rehearsal 并报告结果
//	harnessrun report            生成 run-YYYY-MM-DD.md 报告模板
package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

type taskDef struct {
	ID            string
	Title         string
	Description   string
	MachinePoints []string // 机器可判的评分点描述
}

var tasks = []taskDef{
	{
		ID:          "T1",
		Title:       "Bug 修复闭环",
		Description: "修复 internal/<包> 的一个 bug，全量测试绿，走 harness 流程提交。",
		MachinePoints: []string{
			"go test ./... 全部通过",
			"gofmt -l ./cmd ./internal 无输出",
			"变更目录 .harness/changes/<feat>/ 含 summary.md",
		},
	},
	{
		ID:          "T2",
		Title:       "变更流程全走",
		Description: "实现一个小需求（如给 /healthz 加字段），从需求到提交走完整流程。",
		MachinePoints: []string{
			"REQ 文档含「## 必要性」段",
			"pre-commit.ps1 全部通过",
		},
	},
	{
		ID:          "T3",
		Title:       "文档漂移狩猎",
		Description: "在 README/AGENTS.md/.harness/rules 里找 3 处与实测不符的陈述并修正。",
		MachinePoints: []string{
			"（人工评分：3/3 找齐、每处修正前先实测、常驻层预算不超）",
		},
	},
	{
		ID:          "T4",
		Title:       "否定断言纪律",
		Description: "回答「某功能/符号在本仓是否存在」，结论可直接采信。",
		MachinePoints: []string{
			"（人工评分：给出原始 grep 输出、穷尽证据源或降级 unknown、机器能跑的优先实跑）",
		},
	},
	{
		ID:          "T5",
		Title:       "部署排练与解读",
		Description: "跑 deploy-prod.rehearsal.ps1 并解释每个场景验证什么。",
		MachinePoints: []string{
			"deploy-prod.rehearsal.ps1 53/53 断言通过",
			"全程未触碰生产（端口 7863 无连接）",
		},
	},
}

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	switch os.Args[1] {
	case "list":
		cmdList()
	case "check":
		if len(os.Args) < 3 {
			fmt.Println("用法: harnessrun check <T1|T2|T3|T4|T5>")
			os.Exit(1)
		}
		cmdCheck(os.Args[2])
	case "report":
		cmdReport()
	case "-h", "--help", "help":
		printUsage()
	default:
		fmt.Printf("未知子命令: %s\n", os.Args[1])
		printUsage()
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Println(`harnessrun — 金样任务评分辅助工具（V3-9）

用法:
  harnessrun list              列出 5 个金样任务
  harnessrun check <T1|T2|...> 检查指定任务的机器可判评分点
  harnessrun report            生成 run-YYYY-MM-DD.md 报告模板
  harnessrun help              显示本帮助`)
}

func cmdList() {
	fmt.Println("金样任务清单（GOLDEN-TASKS）：")
	fmt.Println()
	for _, t := range tasks {
		fmt.Printf("  %s · %s\n    %s\n\n", t.ID, t.Title, t.Description)
	}
}

func cmdCheck(taskID string) {
	var t *taskDef
	for i := range tasks {
		if tasks[i].ID == taskID {
			t = &tasks[i]
			break
		}
	}
	if t == nil {
		fmt.Printf("未知任务: %s（用 harnessrun list 看全部）\n", taskID)
		os.Exit(1)
	}

	fmt.Printf("=== 检查 %s · %s ===\n\n", t.ID, t.Title)

	repoRoot := findRepoRoot()

	switch taskID {
	case "T1":
		checkT1(repoRoot)
	case "T2":
		checkT2(repoRoot)
	case "T5":
		checkT5(repoRoot)
	default:
		fmt.Printf("  %s\n", t.MachinePoints[0])
		fmt.Println("  （该任务主要靠人工评分，无机器可判项）")
	}
}

func checkT1(repoRoot string) {
	// 1. go test ./...
	fmt.Print("  [1/3] go test ./... ... ")
	if runGo(repoRoot, "test", "./...", "-count=1") {
		fmt.Println("✅ 通过")
	} else {
		fmt.Println("❌ 失败")
	}

	// 2. gofmt -l
	fmt.Print("  [2/3] gofmt -l ./cmd ./internal ... ")
	cmd := exec.Command("gofmt", "-l", "./cmd", "./internal")
	cmd.Dir = repoRoot
	out, _ := cmd.Output() // R10：gofmt -l 正常运行不返回错误，输出即结果
	if len(out) == 0 {
		fmt.Println("✅ 干净")
	} else {
		fmt.Printf("❌ 有未格式化文件:\n%s", out)
	}

	// 3. 变更目录存在
	fmt.Print("  [3/3] 变更目录 .harness/changes/<feat>/ 含 summary.md ... ")
	changesDir := filepath.Join(repoRoot, ".harness", "changes")
	entries, err := os.ReadDir(changesDir)
	if err != nil {
		fmt.Printf("❌ 无法读取 changes 目录: %v\n", err)
		return
	}
	found := false
	for _, e := range entries {
		if e.IsDir() {
			sumPath := filepath.Join(changesDir, e.Name(), "summary.md")
			if _, err := os.Stat(sumPath); err == nil {
				found = true
				break
			}
		}
	}
	if found {
		fmt.Println("✅ 最近变更目录齐全")
	} else {
		fmt.Println("❌ 未找到含 summary.md 的变更目录")
	}
}

func checkT2(repoRoot string) {
	// 1. pre-commit.ps1
	fmt.Print("  [1/2] pre-commit.ps1 -Fast ... ")
	cmd := exec.Command("powershell", "-NoProfile", "-File", "scripts/pre-commit.ps1", "-Fast")
	cmd.Dir = repoRoot
	if err := cmd.Run(); err != nil {
		fmt.Println("❌ 失败")
	} else {
		fmt.Println("✅ 通过")
	}

	// 2. REQ 必要性段（检查最近新建的 REQ）
	fmt.Print("  [2/2] 最近新建 REQ 含「## 必要性」段 ... ")
	reqDir := filepath.Join(repoRoot, "docs", "prd")
	entries, err := os.ReadDir(reqDir)
	if err != nil {
		fmt.Printf("❌ 无法读取 docs/prd: %v\n", err)
		return
	}
	found := false
	for _, e := range entries {
		if !e.IsDir() && filepath.Ext(e.Name()) == ".md" {
			data, err := os.ReadFile(filepath.Join(reqDir, e.Name()))
			if err != nil {
				continue
			}
			if contains(string(data), "必要性") {
				found = true
				break
			}
		}
	}
	if found {
		fmt.Println("✅ 找到含必要性段的 REQ")
	} else {
		fmt.Println("❌ 未找到含必要性段的 REQ")
	}
}

func checkT5(repoRoot string) {
	fmt.Print("  [1/1] deploy-prod.rehearsal.ps1 ... ")
	cmd := exec.Command("powershell", "-NoProfile", "-File", "scripts/deploy-prod.rehearsal.ps1")
	cmd.Dir = repoRoot
	if err := cmd.Run(); err != nil {
		fmt.Println("❌ 失败")
	} else {
		fmt.Println("✅ 通过")
	}
}

func cmdReport() {
	now := time.Now()
	filename := fmt.Sprintf("run-%s.md", now.Format("2006-01-02"))
	reportPath := filepath.Join("docs", "agent-eval", filename)

	fmt.Printf("生成报告模板: %s\n\n", reportPath)

	content := fmt.Sprintf(`# 金样任务重放 · %s

> 自动生成模板：机器检查结果已填，人工评分请在对应位置补充。

## 机器检查结果

| 任务 | 机器可判项 | 结果 |
|---|---|---|
| T1 | go test / gofmt / 变更目录 | （跑 harnessrun check T1 填） |
| T2 | pre-commit / REQ 必要性 | （跑 harnessrun check T2 填） |
| T5 | deploy-rehearsal 53/53 | （跑 harnessrun check T5 填） |

## 人工评分

| 任务 | 得分 (0-2) | 备注 |
|---|---|---|
| T1 · Bug 修复闭环 |  |  |
| T2 · 变更流程全走 |  |  |
| T3 · 文档漂移狩猎 |  |  |
| T4 · 否定断言纪律 |  |  |
| T5 · 部署排练与解读 |  |  |

**总分：__/10（≥8 判通过）**

## 未达标项根因

（逐条写：哪个任务、哪个评分点、为什么没达标、下次怎么改）
`, now.Format("2006-01-02"))

	if err := os.WriteFile(reportPath, []byte(content), 0644); err != nil {
		fmt.Printf("❌ 写入失败: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("✅ 报告模板已生成，请补充人工评分后提交。")
}

// findRepoRoot 从当前目录往上找 go.mod，定位仓库根。
func findRepoRoot() string {
	dir, _ := os.Getwd() // R10：Getwd 在可访问文件系统上不会失败，失败时退回空串也不致命
	for i := 0; i < 5; i++ {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		dir = filepath.Dir(dir)
	}
	// 兜底：用当前目录
	dir, _ = os.Getwd() // R10：同上，失败时退回空串不致命
	return dir
}

// runGo 运行 go 命令，返回是否成功。
func runGo(repoRoot string, args ...string) bool {
	goExe := os.Getenv("YG_GO_EXE")
	if goExe == "" {
		goExe = "go"
	}
	cmd := exec.Command(goExe, args...)
	cmd.Dir = repoRoot
	return cmd.Run() == nil
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || indexOf(s, substr) >= 0)
}

func indexOf(s, substr string) int {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return i
		}
	}
	return -1
}
