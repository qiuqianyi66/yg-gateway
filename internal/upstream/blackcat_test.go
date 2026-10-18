package upstream

import (
	"fmt"
	"net/http"
	"testing"
	"time"
)

// TestNightChatsGapIsInjectable 钉住 N4 + B4a：夜猫子相邻两次真实对话之间的等待必须
// **可注入**、**生产默认值不得被改小**，且**只等 need-1 次**（末次之后没有下一个请求）。
//
// 为什么要这条用例：`RunNightChats` 里那句 `time.Sleep(4 * time.Second)` 是每次
// `go test ./...` 都要真付的成本（本包单轮从 7s 涨到 11s，其中两处各 4s：本文件的
// RunNightChats 成功路径用例，与 R8 golden 生成器）。它又不能直接删——4s 是「连续对话
// 不贴太近」的实测节奏，删掉是拿上游风控观感换测试速度。所以修法是把它变成可注入的等待。
//
// 牙齿（三条断言互为约束，缺任一条都会退化成假绿）：
//  1. 注入的等待函数**收到的时长必须等于生产默认值 4s** —— 若有人为了跑得快把默认值
//     改小（比如 100ms），这条红；
//  2. 等待次数必须**恰好 need-1**（need=1 → 0 次，need=3 → 2 次）—— 把 `if i < need-1`
//     改回无条件等待（旧行为），或写成 `i < need-2`（少等一次），这张表都有格子红；
//  3. 真实耗时必须远小于等待之和 —— 若注入形同虚设（仍走 time.Sleep），这条红。
func TestNightChatsGapIsInjectable(t *testing.T) {
	for _, need := range []int{1, 2, 3} {
		t.Run(fmt.Sprintf("need=%d", need), func(t *testing.T) {
			sse := "data: {\"id\":\"wb2api-1\",\"choices\":[{\"delta\":{\"content\":\"2\"}}]}\n\ndata: [DONE]\n\n"
			rec := &upstreamhttpRec{}
			c := testClient(upstreamhttpStub(rec, func(q upstreamhttpReq) (*http.Response, error) {
				switch q.Path {
				case "/v2/chat/completions":
					return jsonResp(200, sse), nil
				case "/v2/report":
					return jsonResp(200, `{"code":0,"msg":"OK"}`), nil
				default:
					return nil, fmt.Errorf("unexpected path %s", q.Path)
				}
			}))

			var asked []time.Duration
			c.SetNightChatSleep(func(d time.Duration) { asked = append(asked, d) })

			start := time.Now()
			ok, err := c.RunNightChats(upstreamhttpCNAuth(), need)
			elapsed := time.Since(start)
			if err != nil {
				t.Fatalf("RunNightChats: %v", err)
			}
			if ok != int64(need) {
				t.Fatalf("ok=%d want %d", ok, need)
			}

			// 牙齿 1+2：等待次数恰好 need-1，且时长是生产默认间隔
			if len(asked) != need-1 {
				t.Fatalf("等待次数=%d want %d（只拉相邻两次对话的间距，末次之后没有下一个请求）", len(asked), need-1)
			}
			for i, d := range asked {
				if d != 4*time.Second {
					t.Errorf("第 %d 次等待=%v want 4s（生产默认间隔，改动前是字面量 4s）", i+1, d)
				}
			}
			// 牙齿 3：注入真的生效
			if elapsed > time.Second {
				t.Errorf("注入空等待后耗时 %v，期望 < 1s（若仍走 time.Sleep 需 ≥ %ds）", elapsed, 4*(need-1))
			}
		})
	}
}

// TestNightChatSleepNotPreInjectedInProductionConstruction 钉住「生产构造路径不得预置注入」：
// 若有人为了跑得快在 New() 里塞一个空等待实现，那 4s 节奏就在生产里**静默消失**了——
// 拿上游风控观感换测试速度，而测试本来有自己的注入入口。
func TestNightChatSleepNotPreInjectedInProductionConstruction(t *testing.T) {
	if c := New(); c.nightChatSleep != nil {
		t.Error("New() 不得预置夜间对话等待实现：生产必须走真实的 time.Sleep 节奏")
	}
	if c := (&Client{}); c.nightChatSleep != nil {
		t.Error("字面量构造的 Client 不得带等待实现（零值 = 生产默认行为）")
	}
}
