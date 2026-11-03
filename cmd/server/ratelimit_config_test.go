package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// 本文件补齐 normalize() 里「时长归一」的边界分支（2026-11-03）：
// min_request_interval（主动 RPM 限流）、failure_window 负值钳位、
// degrade_cooldown 空串回落、Load 的 normalize 错误出口、WriteDefault 的
// 父目录创建失败出口。这几处此前只有「正常值」路径被覆盖，负值/非法/空串的
// 兜底分支没人跑过——而兜底分支恰恰是配置写错时唯一拦住带病启动的东西。

// TestPoolMinRequestIntervalNormalize 钉住 min_request_interval / _global 的归一口径：
//
//	键缺席 / 显式 "" → 回落 "0"（关闭，零回归——默认行为必须与未引入该特性时一致）
//	"0"             → 0（显式关闭）
//	"1500ms" / "3s" → 原样解析，且 CN / global 分档互不覆盖（国际版风控更敏感）
//	负值             → 钳 0（= 关闭，不报错：负间隔无数值意义，但值是合法时长）
//	非法时长          → fail fast 报错，且错误带字段路径
//
// 牙齿（**只对错误分支成立，负值钳位不是唯一防线** —— 写清楚免得后人误判）：
//   - 删掉错误包装（config.go:905 / :914）→ 合法 JSON 里写坏时长被静默吞成 0
//     （`time.ParseDuration` 失败返回零值），风控规避配了等于没配，启动不报错。
//     这是本项目「错误不静默吞」（R10）禁的形态，本用例的后两条专门钉它。
//   - 负值钳位（:908 / :917）是**配置层出口口径**，不是唯一防线：下游
//     `pool.minIntervalBlocked` 有 `interval <= 0 → return false`（pick.go:99），
//     `Pool.SetMinRequestInterval` 也自己钳一次负值（pool.go:263-278）。
//     所以删掉本钳位**不会**立刻把选号挂死；本用例钉的是「导出的
//     MinRequestIntervalDur 不得带负值出去」——否则面板/日志/后续消费方
//     会读到一个无法解释的负间隔（属于纵深防御，实测结论见上两处调用点）。
func TestPoolMinRequestIntervalNormalize(t *testing.T) {
	cases := []struct {
		name       string
		json       string
		wantCN     time.Duration
		wantGlobal time.Duration
		wantErr    bool
	}{
		{name: "键缺席回落 0（零回归）", json: `{}`, wantCN: 0, wantGlobal: 0},
		{
			name: "显式空串回落 0",
			json: `{"pool":{"min_request_interval":"","min_request_interval_global":""}}`,
		},
		{
			name: "显式 0 关闭",
			json: `{"pool":{"min_request_interval":"0","min_request_interval_global":"0"}}`,
		},
		{
			name:       "CN/global 分档各自生效",
			json:       `{"pool":{"min_request_interval":"1500ms","min_request_interval_global":"3s"}}`,
			wantCN:     1500 * time.Millisecond,
			wantGlobal: 3 * time.Second,
		},
		{
			name: "负值钳 0 且不报错",
			json: `{"pool":{"min_request_interval":"-5s","min_request_interval_global":"-1s"}}`,
		},
		{name: "非法时长报错（CN）", json: `{"pool":{"min_request_interval":"oops"}}`, wantErr: true},
		{name: "非法时长报错（global）", json: `{"pool":{"min_request_interval_global":"oops"}}`, wantErr: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, err := ParseConfig([]byte(tc.json))
			if tc.wantErr {
				if err == nil {
					t.Fatalf("非法时长应报错，实际解析成功（间隔=%v）", c.MinRequestIntervalDur)
				}
				if !strings.Contains(err.Error(), "min_request_interval") {
					t.Fatalf("错误应带字段路径 pool.min_request_interval，got: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("不应报错: %v", err)
			}
			if c.MinRequestIntervalDur != tc.wantCN {
				t.Errorf("MinRequestIntervalDur = %v, want %v", c.MinRequestIntervalDur, tc.wantCN)
			}
			if c.MinRequestIntervalGlobalDur != tc.wantGlobal {
				t.Errorf("MinRequestIntervalGlobalDur = %v, want %v", c.MinRequestIntervalGlobalDur, tc.wantGlobal)
			}
		})
	}
}

// TestPoolFailureWindowNegativeClamped 负故障窗钳 0（P0-2）。
//
// 与 failure_window="oops" 报错不同：负值是**合法时长**但语义非法（"−5m" 无合理语义），
// 故钳位而非报错。钳成 0 = 关闭故障窗（零回归）。
//
// 牙齿（弱）：`failureWindowBlocked` 判定首位是 `p.failureWindow > 0`（pick.go:89），
// 负值即使漏过配置层也只会落成「关闭」，**不会**变成「恒在窗内、永久避让」。
// 所以本用例钉的是配置层出口口径（`FailureWindowDur` 非负、语义唯一），
// 不是唯一防线——别把它当成「删了钳位就挂」的断言来用。
func TestPoolFailureWindowNegativeClamped(t *testing.T) {
	c, err := ParseConfig([]byte(`{"pool":{"failure_window":"-5s"}}`))
	if err != nil {
		t.Fatalf("负值不应报错（应钳 0）: %v", err)
	}
	if c.FailureWindowDur != 0 {
		t.Fatalf("failure_window=\"-5s\" 应钳 0（关闭），got %v", c.FailureWindowDur)
	}
}

// TestPoolDegradeCooldownEmptyFallsBackToDefault 连败降权时长显式留空时回落默认。
//
// 空串无法 ParseDuration，若不先回落默认，紧随其后的时长解析表（config.go:826）
// 会 fail fast——「留空」直接从「用默认值」变成「启动崩溃」。
//
// 牙齿：删掉 config.go:806-811 的回落 → 本用例报错而非拿到 10m/2h。
func TestPoolDegradeCooldownEmptyFallsBackToDefault(t *testing.T) {
	c, err := ParseConfig([]byte(`{"pool":{"degrade_cooldown":"","degrade_cooldown_max":""}}`))
	if err != nil {
		t.Fatalf("空串应回落默认，不应报错: %v", err)
	}
	if c.Pool.DegradeCooldown != "10m" {
		t.Errorf("degrade_cooldown 空串应回落 \"10m\"，got %q", c.Pool.DegradeCooldown)
	}
	if c.Pool.DegradeCooldownMax != "2h" {
		t.Errorf("degrade_cooldown_max 空串应回落 \"2h\"，got %q", c.Pool.DegradeCooldownMax)
	}
	// 回落必须真的进解析后的字段，否则下游（pool.SetDegrade）拿到的是 0 时长。
	if c.DegradeCooldownDur != 10*time.Minute {
		t.Errorf("DegradeCooldownDur = %v, want 10m", c.DegradeCooldownDur)
	}
	if c.DegradeCooldownMaxD != 2*time.Hour {
		t.Errorf("DegradeCooldownMaxD = %v, want 2h", c.DegradeCooldownMaxD)
	}
}

// TestLoadNormalizeErrorFromEnv 环境变量覆盖带来的非法时长必须在 Load 的
// normalize 阶段报错，而不是带病启动。
//
// 这条覆盖的是 Load 独有的第二个 normalize 出口（config.go:624-626）：
// ParseConfigInto 已经归一过一次（那时 env 还没生效），applyEnv 之后必须再归一次。
//
// 牙齿：把 Load 里 `if err := c.normalize(); err != nil { return nil, err }` 改成
// 忽略错误 → WB2A_SOFT_RATE=oops 静默通过，`SoftRateDur` 保留 ParseDuration 失败时的
// 零值（配置里写的非法基数被吞掉，R10「错误不静默吞」禁的形态），启动日志无异常。
func TestLoadNormalizeErrorFromEnv(t *testing.T) {
	t.Setenv("WB2A_SOFT_RATE", "oops")

	dir := t.TempDir()
	fp := filepath.Join(dir, "c.json")
	if err := os.WriteFile(fp, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Load(fp)
	if err == nil {
		t.Fatal("WB2A_SOFT_RATE 非法时长应在 Load 的 normalize 阶段报错")
	}
	if !strings.Contains(err.Error(), "soft_rate") {
		t.Fatalf("错误应带字段路径 cooldown.soft_rate，got: %v", err)
	}
}

// TestWriteDefaultMkdirFailsOnFilePath 父目录创建失败必须报错。
//
// 场景：目标路径的父级被一个普通文件占住（运维把 config.json 的上级误建成文件，
// 或挂载点异常）。此时若不报错，调用方会以为「默认配置已落盘」继续启动，
// 下次重启又找不到配置——故障延迟到重启才暴露。
//
// 牙齿：删掉 WriteDefault 的 MkdirAll 错误分支 → 本用例拿到 nil error。
func TestWriteDefaultMkdirFailsOnFilePath(t *testing.T) {
	dir := t.TempDir()
	blocker := filepath.Join(dir, "notadir")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	target := filepath.Join(blocker, "sub", "c.json")
	if _, err := WriteDefault(target); err == nil {
		t.Fatalf("父路径是普通文件时应报错，实际成功写入 %s", target)
	}
}
