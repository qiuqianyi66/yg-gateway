package evidence

import "encoding/json"

// jsonMarshal 单独抽出来：让 kind.go 不直接依赖 encoding/json 的调用细节，
// 也便于将来换成更省分配的编码器（当前热路径未用到，YAGNI，保持简单）。
func jsonMarshal(v any) ([]byte, error) { return json.Marshal(v) }
