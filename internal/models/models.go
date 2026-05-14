package models

const (
	StrategyPrimary = "primary"
	StrategyRole    = "role"

	RoleFast       = "fast"
	RoleText       = "text"
	RoleContext    = "context"
	RoleReasoning  = "reasoning"
	RoleStructured = "structured"
	RoleVision     = "vision"

	PrimaryDefault    = "gemma4:e2b"
	FastDefault       = "qwen3.5:0.8b"
	TextDefault       = "ministral-3:3b"
	ContextDefault    = "gemma4:e2b"
	ReasoningDefault  = "qwen3.5:4b"
	StructuredDefault = "nemotron-3-nano:4b"
	VisionDefault     = "gemma4:e2b"
)

func Fallbacks() []string {
	return []string{
		"qwen3.5:2b",
		FastDefault,
	}
}
