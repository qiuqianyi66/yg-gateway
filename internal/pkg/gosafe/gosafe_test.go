package gosafe

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestGoRunsBody：正常路径必须真的执行到。
func TestGoRunsBody(t *testing.T) {
	var done sync.WaitGroup
	done.Add(1)
	var ran atomic.Bool
	Go("test/normal", func() {
		defer done.Done()
		ran.Store(true)
	})
	done.Wait()
	if !ran.Load() {
		t.Fatal("Go 没有执行传入的函数")
	}
}

// TestGoSwallowsPanicAndKeepsGoing：panic 后进程仍能继续跑下一个 goroutine。
// 这是本包存在的唯一理由——没有这条断言，加点 defer 也不能证明它有用。
func TestGoSwallowsPanicAndKeepsGoing(t *testing.T) {
	var done sync.WaitGroup
	done.Add(2)
	Go("test/panics", func() {
		defer done.Done()
		var m map[string]int
		//lint:ignore SA5000 有意向 nil map 写入，制造确定性 panic 验证 Go 的 recover
		m["boom"] = 1
	})
	Go("test/survives", func() {
		defer done.Done()
	})
	done.Wait()
}

// TestRecoverNoPanicIsNoop：没 panic 时 Recover 不做事、不打印。
func TestRecoverNoPanicIsNoop(t *testing.T) {
	defer Recover("test/no-panic")
}

// TestRecoverWithPassesPanicValue：onPanic 必须真的拿到 panic 值，
// 否则调用方无法据它补发错误结果（channel 就永远等不到值）。
func TestRecoverWithPassesPanicValue(t *testing.T) {
	var got any
	func() {
		defer RecoverWith("test/with-panic", func(p any) { got = p })
		panic("needle")
	}()
	if got != "needle" {
		t.Fatalf("onPanic 收到 %v，期望 needle", got)
	}
}

// TestRecoverWithNoPanicSkipsCallback：正常返回时 onPanic 不得被调用，
// 否则会把「没崩」误报成「崩了」。
func TestRecoverWithNoPanicSkipsCallback(t *testing.T) {
	called := false
	func() {
		defer RecoverWith("test/with-no-panic", func(any) { called = true })
	}()
	if called {
		t.Fatal("没有 panic 却调用了 onPanic")
	}
}

// TestRecoverWithNilCallback：onPanic 传 nil 时只记日志，不得二次 panic。
func TestRecoverWithNilCallback(t *testing.T) {
	func() {
		defer RecoverWith("test/with-nil-cb", nil)
		panic("no callback")
	}()
}

// TestRecoverDrainsPanic：defer Recover 能吞掉 panic 并让调用方继续。
// 走到函数末尾就证明 panic 没有穿透——不需要再套一层观察者。
func TestRecoverDrainsPanic(t *testing.T) {
	func() {
		defer Recover("test/drains")
		panic("intentional")
	}()
	time.Sleep(1 * time.Millisecond)
}
