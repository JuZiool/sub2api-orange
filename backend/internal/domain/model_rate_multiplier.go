package domain

// ModelRateMultiplierRule is a group-level exact-match billing multiplier.
// It intentionally has no wildcard semantics.
type ModelRateMultiplierRule struct {
	Model      string  `json:"model"`
	Multiplier float64 `json:"multiplier"`
}
