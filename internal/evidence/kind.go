// Package evidence 事实的四态标记（观察 / 派生 / 推断 / 未知）。
//
// 来源：morluto/rea 的核心纪律——"Distinguish observations, derivations,
// inferences, and unknowns"，以及 "Missing coverage is unknown, not absence"。
//
// 为什么本包存在：网关对外暴露的很多值**看起来是事实，其实是推断**。例如
// 「账号 X 对模型 Y 是收费的」往往只基于两三条 credit 实测样本外推；而
// 「/v1/models 里某个模型没有 supported_efforts」是上游精简目录**没返回**，
// 属于未知，不是"该模型不支持档位"。
//
// 把推断当成观察展示是危险的：消费者（养号决策、面板、agent）会据此做硬性
// 排除——基于噪声把一个健康号踢出选号，而且**不报错**，比功能缺失更难发现。
//
// 设计约束：
//   - 零依赖（domain 层）：本包不 import 任何 internal 包，可被任意层引用；
//   - 零值即 unknown：忘了标注时落到**最保守**的一档，而不是默认当成观察；
//   - 序列化自带 provenance：kind / basis / samples 随值一起出去。
package evidence

// Kind 事实的认识论类别。
type Kind string

const (
	// KindObservation 直接观测到的：上游状态码、账号被禁用、返回的 credit 数值。
	KindObservation Kind = "observation"
	// KindDerivation 从观察值算出来的，过程无损或误差已知：EWMA 平滑值、
	// 均值、分桶插值得到的分位数。
	KindDerivation Kind = "derivation"
	// KindInference 基于有限样本的推测，存在置信度问题：成本 tier 判定、
	// 按公开价折算的 USD 价值。
	KindInference Kind = "inference"
	// KindUnknown 不知道，且能说明需要什么才能知道。
	// 注意：未知 ≠ 否。目录没声明 supports_images 是 unknown，不是"不支持"。
	KindUnknown Kind = "unknown"
)

// Fact 一个带来源标注的事实。
//
// Value 是值本身（具体类型由调用方决定，故用 any）。
// Samples 只在 KindInference / KindDerivation 下有意义（样本数），
// unknown 与纯 observation 下为 0 且序列化时省略。
type Fact struct {
	Value   any
	Kind    Kind
	Basis   string
	Samples int
}

// Observe 记录一个直接观测值。basis 说明观测来源（如 "upstream status"）。
func Observe(v any, basis string) Fact {
	return Fact{Value: v, Kind: KindObservation, Basis: basis}
}

// Derive 记录一个派生值。samples 是参与派生的样本数（0 表示不适用/未知）。
func Derive(v any, basis string, samples int) Fact {
	return Fact{Value: v, Kind: KindDerivation, Basis: basis, Samples: samples}
}

// Infer 记录一个推断值。**必须**给 samples：推断的可信度全靠样本数表达，
// 不给样本数的推断等于让消费者盲信。
func Infer(v any, basis string, samples int) Fact {
	return Fact{Value: v, Kind: KindInference, Basis: basis, Samples: samples}
}

// Unknown 记录一个未知。basis 在这里的语义是「需要什么才能消解」
// （如 "需要一次上游返回 credit 的调用"），与已知事实的「来源」不同——
// 这是刻意的复用：两种情况下 basis 都回答"这个结论凭什么"。
func Unknown(basis string) Fact {
	return Fact{Kind: KindUnknown, Basis: basis}
}

// IsUnknown 报告该事实是否未知（含零值）。
//
// 零值 Fact 的 Kind 是 ""，也判为 unknown——这样"忘了标注"落到最保守档。
func (f Fact) IsUnknown() bool {
	return f.Kind != KindObservation && f.Kind != KindDerivation && f.Kind != KindInference
}

// MarshalJSON 序列化为带 provenance 的对象。
//
// 为什么不直接用 struct tag：unknown 态要省掉 value/samples（没有值），
// 而 observation 态不需要 samples（不适用）。手工构造 map 才能让不同 Kind
// 输出不同字段集，避免消费者看到 `"value":null` 这种误导性字段。
func (f Fact) MarshalJSON() ([]byte, error) {
	m := map[string]any{"kind": string(nonEmptyKind(f.Kind))}
	if f.Basis != "" {
		m["basis"] = f.Basis
	}
	if !f.IsUnknown() && f.Value != nil {
		m["value"] = f.Value
	}
	if f.Samples > 0 && (f.Kind == KindDerivation || f.Kind == KindInference) {
		m["samples"] = f.Samples
	}
	return jsonMarshal(m)
}

func nonEmptyKind(k Kind) Kind {
	if k == "" {
		return KindUnknown
	}
	return k
}
