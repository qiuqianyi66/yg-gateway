// panel_syncnick.go 昵称同步（issue #94：上游改名免重登）。
//
// 从 panel.go 拆出（2026-11-17）：那段是本文件里唯一一处并发拉取上游资料的地方，
// 独立成文件后 panel.go 才有空间给 goroutine 加崩溃隔离——体量棘轮只许收紧，
// 不能靠抬阈值腾地方。
package panel

import (
	"log"
	"sync"

	"yg-gateway/internal/auth"
	"yg-gateway/internal/pkg/gosafe"
)

// syncNicknames 手动刷新时的昵称同步。
// 只在面板手动「刷新」路径调用——后台余额定时器不触发（用户明确要求资料接口
// 仅手动触达）。逐号拉 /console/account，只取 nickname（手机号等敏感字段在
// upstream.FetchAccountProfile 内即被丢弃）；单号失败静默跳过，不打断余额刷新
// 的既有结果。
func (p *Panel) syncNicknames() {
	type job struct {
		uid string
		a   *auth.Auth
	}
	var jobs []job
	for _, st := range p.cfg.Pool.List() {
		if st.Disabled {
			continue
		}
		if a := p.cfg.Pool.AuthByUID(st.UID); a != nil && a.AccessTokenValue() != "" {
			jobs = append(jobs, job{uid: st.UID, a: a})
		}
	}
	if len(jobs) == 0 {
		return
	}
	var (
		mu      sync.Mutex
		updated int
		failed  int
		sem     = make(chan struct{}, 3)
		wg      sync.WaitGroup
	)
	for _, j := range jobs {
		wg.Add(1)
		go func(j job) {
			defer wg.Done()
			defer gosafe.Recover("panel/sync-nicknames")
			sem <- struct{}{}
			defer func() { <-sem }()
			nick, err := p.cfg.Upstream.FetchAccountProfile(j.a)
			if err != nil {
				mu.Lock()
				failed++
				mu.Unlock()
				return
			}
			if p.cfg.Pool.SetNickname(j.uid, nick) {
				mu.Lock()
				updated++
				mu.Unlock()
			}
		}(j)
	}
	wg.Wait()
	if updated > 0 || failed > 0 {
		log.Printf("panel: 昵称同步：更新 %d 个，失败 %d 个（未变化不计数）", updated, failed)
	}
}
