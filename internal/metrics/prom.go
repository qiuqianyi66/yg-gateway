// prom.go Prometheus 文本格式暴露（F-710）。
//
// 为什么自实现而不是引 prometheus/client_golang：项目哲学是「最少依赖」（当前
// 仅 12 个模块），而本网关需要的指标面很窄（请求计数 / 在途 / 延迟分桶 / 少量
// gauge），text exposition format 本身足够简单——为十几行文本引一个带 registry、
// collector、pushgateway 的库不成比例。
//
// 文本格式要点（https://prometheus.io/docs/instrumenting/exposition_formats/）：
//   - `# HELP name 说明` 与 `# TYPE name counter|gauge|histogram` 是可选元数据；
//   - 样本行 `name{label="v",...} value`；无标签时可省略大括号；
//   - 直方图 = 每个桶一行 `_bucket{le="..."}` + `_sum` + `_count`；
//   - 数值用 Go 的 %g 输出（Prometheus 接受科学计数法与整数）。
//
// 并发：Builder 只在单个请求的处理 goroutine 内使用（/metrics 抓取时），
// 无共享状态；被读的计数器本身都是 atomic 或自带锁的。
package metrics

import (
	"fmt"
	"io"
	"sort"
	"strings"
)

// Prometheus 文本暴露的构造器。
type Prometheus struct {
	sb strings.Builder
}

// NewPrometheus 构造一个空的暴露文本构造器。
func NewPrometheus() *Prometheus { return &Prometheus{} }

// Help 写一条 HELP 元数据（重复写同名指标时后者覆盖，无害）。
func (p *Prometheus) Help(name, help string) *Prometheus {
	fmt.Fprintf(&p.sb, "# HELP %s %s\n", name, help)
	return p
}

// Type 写一条 TYPE 元数据。
func (p *Prometheus) Type(name, typ string) *Prometheus {
	fmt.Fprintf(&p.sb, "# TYPE %s %s\n", name, typ)
	return p
}

// Counter 写一个 counter/gauge 样本。labels 为 "k=v" 对（已转义）。
func (p *Prometheus) Counter(name string, v int64, labels ...string) *Prometheus {
	p.sample(name, formatInt(v), labels...)
	return p
}

// GaugeFloat 写一个浮点 gauge 样本。
func (p *Prometheus) GaugeFloat(name string, v float64, labels ...string) *Prometheus {
	p.sample(name, formatFloat(v), labels...)
	return p
}

// Histogram 把 Histogram 暴露成 `_bucket{le=...}` + `_sum` + `_count`。
//
// le 用毫秒整数上界；+Inf 桶用 "+Inf"（Prometheus 约定）。与 /status 的
// 分位数快照**同一份数据、两种口径**：/status 给「P95 是多少毫秒」，
// /metrics 给「落在各桶的样本数」——后者可被 PromQL 直接算分位数与速率。
func (p *Prometheus) Histogram(name string, h *Histogram, labels ...string) *Prometheus {
	if h == nil {
		return p
	}
	var cum int64
	for i := 0; i < bucketCount; i++ {
		cum += h.buckets[i].Load()
		if i == bucketCount-1 {
			// +Inf 桶：le="+Inf"（Prometheus 约定），值同样是累计计数。
			p.sample(name+"_bucket", formatInt(cum), append(labels, `le="+Inf"`)...)
			continue
		}
		le := bucketUpperMs(i)
		p.sample(name+"_bucket", formatInt(cum), append(labels, fmt.Sprintf(`le="%g"`, le))...)
	}
	p.sample(name+"_sum", formatFloat(float64(h.sumMs.Load())), labels...)
	p.sample(name+"_count", formatInt(h.count.Load()), labels...)
	return p
}

// WriteTo 把已构造的文本写入 w。
func (p *Prometheus) WriteTo(w io.Writer) (int64, error) {
	n, err := io.WriteString(w, p.sb.String())
	return int64(n), err
}

// String 返回已构造的文本（测试用）。
func (p *Prometheus) String() string { return p.sb.String() }

// sample 写一行样本；labels 为空时省略大括号。
func (p *Prometheus) sample(name, value string, labels ...string) {
	if len(labels) == 0 {
		fmt.Fprintf(&p.sb, "%s %s\n", name, value)
		return
	}
	sorted := append([]string(nil), labels...)
	sort.Strings(sorted) // 稳定顺序：同名指标多次抓取时 diff 干净
	fmt.Fprintf(&p.sb, "%s{%s} %s\n", name, strings.Join(sorted, ","), value)
}

// formatInt 整数格式化（避免 strconv 依赖——fmt 已足够）。
func formatInt(v int64) string { return fmt.Sprintf("%d", v) }

// formatFloat 浮点格式化：去掉无意义的小数尾巴（1.5 而非 1.500000），
// 大数不走科学计数法以外的形态（Prometheus 都接受）。
func formatFloat(v float64) string { return fmt.Sprintf("%g", v) }
