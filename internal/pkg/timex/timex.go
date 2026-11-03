// Package timex 时间相关的跨包原语：布局常量、本地日/时刻格式化、可取消等待。
//
// 为什么单独成包（实测依据：docs/prd/REQ-yg-gateway-code-reuse-2026-11-02.md）：
//   - "2006-01-02" 字面量在 10 个包散落 15 处、"2006-01-02 15:04:05" 4 处。改日期口径
//     （统一时区、改分隔符）要逐个翻，漏一处就是面板与台账对不上——且不报错。
//   - SleepCtx 在 server 与 scheduler 各有一份等价实现。依赖方向禁止两者互引
//     （check-boundaries.ps1 白名单），原实现注释已自认是「消费侧实现」的复制品；
//     纯叶子包是唯一能消掉这份复制、又不破依赖方向的位置。
//
// 本包是**纯叶子**：零 internal 依赖，只用标准库，可被任意层引用（白名单层名 'pkg'）。
// 判据：这里只放「与业务语义无关、多个包会各自写一遍」的东西；带业务口径的（如
// 签到日、计费周期、CST 零点）留在各自领域包，别搬进来。
package timex

import (
	"context"
	"time"
)

// 布局常量。选它们是因为**已经是既成事实**（全仓 wire 格式、文件名、分片键都按此
// 落地），不是"更好看的格式"——改这里的值等于改对外契约，必须同步所有消费方。
const (
	// DateLayout 本地日期（分片键 d:、日志文件名、趋势图日期列）。
	DateLayout = "2006-01-02"
	// DateTimeLayout 本地日期时间（计费周期上下界、软限流到期时刻）。
	DateTimeLayout = "2006-01-02 15:04:05"
	// HourLayout 小时分片键（usage 的 h: 前缀）——与 HourStart 必须成对使用，
	// 否则"写进 h: 的桶"和"按小时截断查的窗口"会错开一格。
	HourLayout = "2006-01-02T15"
)

// Date 按本地时区（t 自身时区）格式化日期。
func Date(t time.Time) string { return t.Format(DateLayout) }

// DateTime 按本地时区格式化日期时间。
func DateTime(t time.Time) string { return t.Format(DateTimeLayout) }

// Hour 按本地时区格式化小时分片键。
func Hour(t time.Time) string { return t.Format(HourLayout) }

// HourStart 把时刻截断到所属小时的起点（保留 t 的时区）。
//
// 与 HourLayout 的不变式：Hour(HourStart(t)) == Hour(t)。usage 的写入侧用 Hour(now)
// 生成分片键、查询侧用 HourStart(now) 算窗口下界，两侧必须同源，否则窗口少算/多算一格。
func HourStart(t time.Time) time.Time {
	return t.Truncate(time.Hour)
}

// SleepCtx 可取消的等待：ctx 取消立即返回 false（客户端断连 / 优雅停机不必等退避睡醒），
// 等满返回 true。d<=0 立即放行（测试里把退避基数置 0 就靠这条，语义必须与"零等待"一致）。
//
// 为什么 ctx.Err()==nil 而不是恒 true：d<=0 时若 ctx 已取消，调用方仍需得到 false
// ——否则"已取消的上下文"会被当成"等待成功"，轮转/限速循环会多跑一轮。
func SleepCtx(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return ctx.Err() == nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
