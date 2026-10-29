// bench 网关级 benchmark 命令行工具（REQ-benchmark-2026-10-29）。
//
// 读取 reqlog 归档 + 账号现态快照目录，输出三项可验证指标
// （选号到期偏置 M1 / 池健康度 M2 / 路由成功率 M3）到 JSON。
// 纯离线计算，不发起任何上游调用。
//
// 用法：
//
//	bench -archive <reqlog目录> -snapshots <快照目录> [-out <json>] [-window <天>]
//
// 数据源：
//   - -archive：reqlog 归档目录（requests-YYYY-MM-DD.jsonl），生产
//     D:/yg-gateway/data/request-logs；
//   - -snapshots：账号现态快照目录（REQ-snapshot 落盘），生产
//     D:/yg-gateway/data/snapshots；缺省 = 不叠加账号现态（M1 样本可视为 0）。
//
// 输出：JSON Report（含四态标注 + 样本数），-out 缺省打 stdout。
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"yg-gateway/internal/bench"
	"yg-gateway/internal/reqlog"
)

func main() {
	archiveDir := flag.String("archive", "", "reqlog 归档目录（requests-*.jsonl）")
	snapshotDir := flag.String("snapshots", "", "账号现态快照目录（YYYY-MM-DD.json）")
	out := flag.String("out", "", "输出文件（缺省 stdout）")
	flag.Parse()

	if *archiveDir == "" {
		log.Fatal("-archive 必填：reqlog 归档目录")
	}

	rec := reqlog.New(reqlog.Config{Dir: *archiveDir, Enabled: true, RetentionDays: 0})
	events, err := rec.ReadAll(reqlog.Filter{})
	if err != nil {
		log.Fatalf("读归档失败: %v", err)
	}
	accounts := readSnapshots(*snapshotDir)

	rep := bench.Eval(events, accounts, time.Now())
	data, err := json.MarshalIndent(rep, "", "  ")
	if err != nil {
		log.Fatalf("序列化失败: %v", err)
	}
	if *out != "" {
		if err := os.WriteFile(*out, append(data, '\n'), 0o644); err != nil {
			log.Fatalf("写输出失败: %v", err)
		}
		fmt.Printf("报告已写入 %s（事件 %d 条，窗口 %d 天）\n", *out, rep.Events, rep.WindowDays)
	} else {
		fmt.Println(string(data))
	}
}

// readSnapshots 读取快照目录中**最新**一份（按文件名日期排序取最大），
// 投影为 bench.AccountState。目录空/不存在 → 空列表（M1 样本 0）。
func readSnapshots(dir string) []bench.AccountState {
	if dir == "" {
		return nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		log.Printf("snapshots 目录读取失败（忽略，M1 无账号现态）: %v", err)
		return nil
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if strings.HasSuffix(e.Name(), ".json") && !strings.HasSuffix(e.Name(), ".tmp") {
			names = append(names, e.Name())
		}
	}
	if len(names) == 0 {
		return nil
	}
	sort.Strings(names) // YYYY-MM-DD.json 字典序 = 时间序
	latest := filepath.Join(dir, names[len(names)-1])
	raw, err := os.ReadFile(latest)
	if err != nil {
		log.Printf("读快照 %s 失败: %v", latest, err)
		return nil
	}
	var snap struct {
		Accounts []bench.AccountState `json:"accounts"`
	}
	if err := json.Unmarshal(raw, &snap); err != nil {
		log.Printf("解析快照 %s 失败: %v", latest, err)
		return nil
	}
	if len(snap.Accounts) == 0 {
		return nil
	}
	log.Printf("快照源：%s（%d 账号）", latest, len(snap.Accounts))
	return snap.Accounts
}
