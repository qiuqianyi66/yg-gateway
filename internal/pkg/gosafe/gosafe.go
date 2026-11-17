// Package gosafe 崩溃隔离：让后台 goroutine 里的 panic 只损失一次任务，不损失进程。
// 与 internal/pkg/timex 同级（叶子包，不依赖任何业务包，因此所有业务包都可以引用它）。
//
// 为什么需要：网关是 7×24 常驻服务，HTTP 请求有 net/http 自带的 per-request recover，
// 但后台 goroutine（排程任务、养号队列、落盘、会话清理）不在任何请求栈上——那里
// 的一次 nil 解引用会直接终止整个进程。加上防护后最坏结果是「这一轮任务失败」。
//
// 纪律（不是静默吞错）：recover 后必须把 panic 值与调用栈打进日志。吞掉不报
// 等于把 bug 永久隐藏，比崩进程更难查。
package gosafe

import (
	"log"
	"runtime/debug"
)

// Go 启动一个带崩溃隔离的 goroutine。
// name 用于日志定位（写「谁崩了」，不要写 "goroutine" 这种无信息量的词）。
func Go(name string, fn func()) {
	go func() {
		defer Recover(name)
		fn()
	}()
}

// Recover 供已经在 goroutine 内、需要复用同一套记录格式的调用方 defer 使用。
//
// 用法：
//
//	go func() {
//	    defer gosafe.Recover("scheduler/taskCheckin")
//	    ...
//	}()
func Recover(name string) {
	p := recover()
	if p == nil {
		return
	}
	log.Printf("[panic] %s: %v\n%s", name, p, debug.Stack())
}

// RecoverWith 在记录日志之后，把 panic 值交给调用方善后。
//
// 为什么需要单独一个：有些 goroutine 的结果是通过 channel 交回的，panic 后
// 若只是记日志，接收端会永远等一个不会到的值——recover 救了进程却卡死了请求。
// 这种情况必须由调用方在 onPanic 里补发一个错误结果（收到结果再关闭 channel）。
//
// 用法：
//
//	go func() {
//	    defer gosafe.RecoverWith("upstream/models-probe", func(p any) {
//	        ch <- probeResult{err: fmt.Errorf("probe panicked: %v", p)}
//	    })
//	    ch <- probe()
//	}()
func RecoverWith(name string, onPanic func(any)) {
	p := recover()
	if p == nil {
		return
	}
	log.Printf("[panic] %s: %v\n%s", name, p, debug.Stack())
	if onPanic != nil {
		onPanic(p)
	}
}
