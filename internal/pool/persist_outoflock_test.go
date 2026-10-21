package pool

// P2 回归：落盘重活（序列化 + 磁盘 IO）移出 p.mu 后的行为钉死。
// 背景：旧实现持 p.mu 做全量 JSON 序列化 + 写文件 + Redis 镜像，把选号临界区
// 拉长到磁盘 IO 量级；改成「持锁收集快照 → 解锁 → saveMu 串行化落盘」两段式。
// 这三个用例分别钉住：① 锁外可调（防回退成锁内落盘——RWMutex 不可重入，
// 回退即自死锁）；② 与选号并发无死锁无竞争；③ Remove 的两段式不改变持久化结果。

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"yg-gateway/internal/auth"
)

// TestSaveUnlockedRunsOutsidePoolLock：持锁调用 saveUnlocked 也必须立即完成。
// 这是 P2 的不变量钉子——正确的两段式实现此时只碰 saveMu，与 p.mu 无关；
// 若有人把序列化/写盘挪回 p.mu 内（或让 saveUnlocked 自己去拿 p.mu），
// RWMutex 不可重入 → 本用例永久挂起 → 5s 超时失败。
// （生产形态是「解锁后调用」，那个形态对两种实现都畅通、咬不住回退，
//
//	所以这里刻意选持锁调用——只有它能让错误的锁假设现形。）
func TestSaveUnlockedRunsOutsidePoolLock(t *testing.T) {
	dir := t.TempDir()
	fp := filepath.Join(dir, "state.json")
	p := New(fp)
	defer p.Close()
	p.Add(&auth.Auth{UID: "u1"})
	p.SetCredits("u1", 42, 0)

	p.mu.Lock()
	job, ok := p.snapshotForSaveLocked()
	if !ok {
		p.mu.Unlock()
		t.Fatal("SetCredits 后应收集到脏快照")
	}

	done := make(chan struct{})
	go func() {
		p.saveUnlocked(job) // 持 p.mu 调用：实现若不依赖 p.mu 就应立即完成
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		p.mu.Unlock()
		t.Fatal("saveUnlocked 持锁调用死锁（实现回退成锁内落盘了）")
	}
	p.mu.Unlock()

	// 落盘不阻塞选号：锁已释放，Pick 立即可用。
	if p.Pick() == nil {
		t.Fatal("落盘后 Pick 不可用")
	}
	raw, err := os.ReadFile(fp)
	if err != nil || !strings.Contains(string(raw), `"credits": 42`) {
		t.Fatalf("state.json 未写出: %v %s", err, raw)
	}
}

// TestFlushConcurrentWithPickNoDeadlock：选号（持 p.mu 读锁）与 Flush
// （持锁收集 → 锁外落盘）并发互踩。-race 下应无竞争；saveMu 串行化落盘，
// 不应出现写盘交叉损坏；两侧都能在超时前退出（锁序单向，无死锁面）。
func TestFlushConcurrentWithPickNoDeadlock(t *testing.T) {
	dir := t.TempDir()
	fp := filepath.Join(dir, "state.json")
	p := New(fp)
	defer p.Close()
	for i := 0; i < 4; i++ {
		p.Add(&auth.Auth{UID: fmt.Sprintf("u%d", i)})
	}
	p.SetCredits("u0", 42, 0) // 置 dirty，保证 Flush 有活干

	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { // 选号侧
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				if a := p.Pick(); a != nil {
					p.Release(a.UID)
				}
			}
		}
	}()
	go func() { // 落盘侧
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				p.Flush()
			}
		}
	}()
	time.Sleep(150 * time.Millisecond)
	close(stop)

	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Flush 与 Pick 并发死锁（锁序坏了？）")
	}

	raw, err := os.ReadFile(fp)
	if err != nil || !strings.Contains(string(raw), `"credits": 42`) {
		t.Fatalf("并发期间 state.json 未写出或内容损坏: %v %s", err, raw)
	}
}

// TestRemovePersistsViaTwoStageSave：Remove 改两段式（显式解锁替代 defer）后，
// 持久化结果不变——被删账号不得在重启后复活；且 Remove 返回时锁已释放
// （若把 saveUnlocked 挪回锁内，Pick 会跟在落盘后面排队，本用例不直接测时延，
// 测的是正确性与无死锁）。
func TestRemovePersistsViaTwoStageSave(t *testing.T) {
	dir := t.TempDir()
	fp := filepath.Join(dir, "state.json")
	p := New(fp)
	defer p.Close()
	p.Add(&auth.Auth{UID: "u1"})
	p.Add(&auth.Auth{UID: "u2"})
	p.Flush()

	removed := p.Remove("u1")
	if removed == nil || removed.UID != "u1" {
		t.Fatalf("Remove 返回 %+v，期望 u1 凭证", removed)
	}
	if got := p.Remove("nope"); got != nil {
		t.Fatalf("Remove 不存在账号应返回 nil，got %+v", got)
	}
	if p.Pick() == nil {
		t.Fatal("Remove 后 Pick 不可用")
	}

	p2 := New(fp)
	defer p2.Close()
	if _, ok := p2.Status("u1"); ok {
		t.Fatal("被删账号在重启后复活（两段式落盘漏了？）")
	}
	if _, ok := p2.Status("u2"); !ok {
		t.Fatal("保留账号丢失（两段式落盘多删了？）")
	}
}
