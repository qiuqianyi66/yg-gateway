// snapshot.go 账号现态每日快照（历史存活曲线数据地基，REQ-snapshot）。
//
// 每日 03:00 把 Pool.List() 全量现态落盘为 <SnapshotDir>/YYYY-MM-DD.json（原子写：
// tmp + rename，防半截文件）。四态：快照内容是 Pool 直接观测的当前状态
// （observation）；「历史存活曲线 / 到期曲线」由跨日文件合成——本版只落盘不消费，
// 曲线展示与消费是后续轮次（REQ 已标 unknown + requires 快照积累）。
//
// 边界纪律：
//   - R1：只写 Pool.List() 的公开状态字段（uid/昵称/积分/到期/健康度/EWMA），
//     不含任何凭证（token/密码），与 /panel/api/status 同口径；
//   - R10：落盘失败不静默——log 一行并继续（快照是尽力而为的观测，失败不阻断
//     排程循环；但必须可见，运维据此知道数据地基断了）。
package scheduler

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"time"
)

// snapshotNow 快照所用时钟（生产恒为 time.Now；测试钉住日期，避免依赖墙钟
// 造成「今日文件名」断言随午夜翻转而失效）。
var snapshotNow = time.Now

// RunSnapshotNow 立即落盘一份账号现态快照（由 taskSnapshot 排程触发；
// 亦可手动调用）。目录未配置（SnapshotDir 空）时直接返回（任务未启用）。
func (s *Scheduler) RunSnapshotNow() {
	s.schedMu.Lock()
	dir := s.cfg.SnapshotDir
	s.schedMu.Unlock()
	if dir == "" {
		return
	}
	now := snapshotNow()
	// 全量读取发生在落盘时刻；Pool.List() 内部自锁，无需在此额外加锁。
	statuses := s.cfg.Pool.List()
	payload := map[string]any{
		"captured_at": now.Format(time.RFC3339),
		"accounts":    statuses,
	}
	data, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		log.Printf("snapshot: marshal failed: %v", err)
		return
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		log.Printf("snapshot: mkdir %s: %v", dir, err)
		return
	}
	name := filepath.Join(dir, now.Format("2006-01-02")+".json")
	tmp := name + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		log.Printf("snapshot: write %s: %v", tmp, err)
		return
	}
	if err := os.Rename(tmp, name); err != nil {
		log.Printf("snapshot: rename %s -> %s: %v", tmp, name, err)
		return
	}
	log.Printf("snapshot: %d 账号现态已落盘 %s", len(statuses), name)
}
