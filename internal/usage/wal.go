package usage

import (
	"bufio"
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"strings"
)

// WAL（usage.wal.jsonl）是增量落库的追加日志（R-205）。
//
// 为什么需要：旧 flush 每 30s 把全部桶（可达 400k × ~90B ≈ 数十 MB）整体深拷贝 +
// 重新序列化 + tmp+rename——与「桶数」成正比，大桶场景每次都全量重写。WAL 把落盘拆成
// 两段：日常 flush 只把**本轮脏桶**追加进 JSONL（append，小）；完整 usage.json 基线快照
// 只在 Stop / Save / WAL 超过阈值时重写一次（写完截断 WAL）。
//
// 崩溃安全：WAL 追加即落盘（durable）；基线快照仍走 tmp+rename 原子替换。崩溃在任意时刻：
// 旧基线 + WAL 未截断部分都还在，重启时「加载基线 → 回放 WAL」即可完整恢复。
// 回放合并规则：计数字段单调非减，按**逐字段取大**合并（见 mergeBucketMax）——这同时消解了
// 「旧版回滚后写基线、再升级 WAL 回放覆盖」的时序回退（max 不退化）。

// walFileVersion WAL 格式版本。首行写入，旧程序不认则忽略（不影响主文件加载）。
const walFileVersion = 1

// walCheckpointAfter 累计多少条 WAL 记录后强制重写一次完整基线（截断 WAL）。
// 日常 flush 只追加；只有基线会把全量桶重写——阈值防止长跑时 WAL 无限增长。
const walCheckpointAfter = 20000

// walPath 由主文件路径推出 WAL 路径（usage.json → usage.wal.jsonl，同目录）。
func walPathOf(mainPath string) string {
	if mainPath == "" {
		return ""
	}
	return strings.TrimSuffix(mainPath, ".json") + ".wal.jsonl"
}

// walRecord 一条 WAL 记录（二选一：桶 upsert 或月累计 upsert）。
type walRecord struct {
	B *bucket `json:"b,omitempty"` // 桶全量当前值（按 key upsert）
	M string  `json:"m,omitempty"` // monthly 键（"YYYY-MM|keyID"）
	V int64   `json:"v,omitempty"` // monthly 值
}

// appendWAL 把一批记录追加进 WAL 文件（调用方持锁；写盘失败仅记日志——观测层，
// 不阻断 Add；下一轮 flush 会补写同一批脏键）。
func appendWAL(path string, recs []walRecord) {
	if len(recs) == 0 {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		log.Printf("[usage] WAL 建目录失败: %v", err)
		return
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		log.Printf("[usage] WAL 打开失败: %v", err)
		return
	}
	bw := bufio.NewWriter(f)
	for i := range recs {
		line, err := json.Marshal(&recs[i])
		if err != nil {
			continue
		}
		bw.Write(line)
		bw.WriteByte('\n')
	}
	if err := bw.Flush(); err != nil {
		log.Printf("[usage] WAL 刷写失败: %v", err)
	}
	if err := f.Close(); err != nil {
		log.Printf("[usage] WAL 关闭失败: %v", err)
	}
}

// truncateWAL 清空 WAL（基线快照写完后调用——此后基线已包含 WAL 全部内容）。
func truncateWAL(path string) {
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		log.Printf("[usage] WAL 截断失败: %v", err)
	}
}

// replayWAL 回放 WAL 到内存桶/月累计（加载基线后调用）。坏行（半截追加崩溃残留）跳过。
// 合并规则：逐字段取大（计数单调非减），消解回滚时序回退。
func replayWAL(path string, buckets map[string]*bucket, monthly map[string]int64) {
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return
		}
		log.Printf("[usage] WAL 读取失败（跳过回放）: %v", err)
		return
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	n := 0
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var rec walRecord
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			// 崩溃时可能留下半行：跳过坏行，保住其余记录。
			continue
		}
		if rec.B != nil {
			k := bucketKey(rec.B.Scope, rec.B.Realm, rec.B.UID, rec.B.Model, rec.B.Rate, rec.B.Key)
			mergeBucketMax(buckets, k, rec.B)
			n++
		} else if rec.M != "" {
			if v, ok := monthly[rec.M]; !ok || rec.V > v {
				monthly[rec.M] = rec.V
			}
		}
	}
	if n > 0 {
		log.Printf("[usage] WAL 回放 %d 条桶记录", n)
	}
}

// mergeBucketMax 把 WAL 桶记录逐字段取大合并进既有桶（计数单调非减；
// 解决「旧版回滚写了更新的基线、再升级被旧 WAL 覆盖」的时序回退）。
func mergeBucketMax(buckets map[string]*bucket, k string, rec *bucket) {
	existing := buckets[k]
	if existing == nil {
		cp := *rec
		buckets[k] = &cp
		return
	}
	if existing.Req < rec.Req {
		existing.Req = rec.Req
	}
	if existing.Err < rec.Err {
		existing.Err = rec.Err
	}
	if existing.PT < rec.PT {
		existing.PT = rec.PT
	}
	if existing.CT < rec.CT {
		existing.CT = rec.CT
	}
	if existing.TT < rec.TT {
		existing.TT = rec.TT
	}
	if existing.LatMs < rec.LatMs {
		existing.LatMs = rec.LatMs
	}
	if existing.LatN < rec.LatN {
		existing.LatN = rec.LatN
	}
	if existing.TPS < rec.TPS {
		existing.TPS = rec.TPS
	}
	if existing.TPSN < rec.TPSN {
		existing.TPSN = rec.TPSN
	}
	if existing.CR < rec.CR {
		existing.CR = rec.CR
	}
	if existing.CRN < rec.CRN {
		existing.CRN = rec.CRN
	}
	if existing.CRT < rec.CRT {
		existing.CRT = rec.CRT
	}
	if existing.CH < rec.CH {
		existing.CH = rec.CH
	}
	if existing.CM < rec.CM {
		existing.CM = rec.CM
	}
}
