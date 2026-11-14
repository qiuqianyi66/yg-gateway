// OneLineWriter：把「每条日志消息内部的换行」压成空格，保留末尾那一个换行。
//
// 为什么放在写出口而非逐个调用点（2026-11-14，防御纵深）：
//   - log.Printf 的参数字段可能来自客户端（model、请求 ID、错误 payload、上游 body），
//     逐个调用点清洗必然漏（本仓 17+ 处 log.Printf 带外部字符串，新增一处就漏一处）；
//   - 写出口是**唯一必经点**：包在 log.SetOutput 的 MultiWriter 外层后，所有现在和
//     将来的日志都自动单行化，不再依赖调用者记得清洗。
//
// 与 logfmt.OneLine 的分工：OneLine 负责字符串级压平（表格行等直接 Fprintf 的路径用），
// 本 writer 负责「消息级」兜底（一次 Write = 一条日志，只保留末字节的换行）。
package logfmt

import (
	"bytes"
	"io"
)

// OneLineWriter 包装底层 Writer，逐条把消息内部的控制字符压平为空格。
type OneLineWriter struct {
	W io.Writer
}

// Write 实现 io.Writer。返回值恒为 len(p)（即使底层写失败也报已消费全部），
// 与 log 包的调用约定一致：log 只关心「写没写出去」，不重试。
func (w OneLineWriter) Write(p []byte) (int, error) {
	if len(p) == 0 || w.W == nil {
		return len(p), nil
	}
	msg := p
	// 末尾换行是日志条目的正常分隔符，必须保留；只压平消息体内部的换行。
	trailing := byte(0)
	if last := p[len(p)-1]; last == '\n' || last == '\r' {
		trailing = last
		msg = p[:len(p)-1]
	}
	// 快速路径：消息体本身干净（绝大多数日志）→ 零拷贝直通。
	if !bytes.ContainsAny(msg, "\r\n\t\x00\x7f") {
		_, err := w.W.Write(p)
		return len(p), err
	}
	flat := OneLine(string(msg))
	if trailing != 0 {
		flat += string(trailing)
	}
	_, err := w.W.Write([]byte(flat))
	return len(p), err
}
