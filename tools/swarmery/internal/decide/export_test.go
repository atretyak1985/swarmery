package decide

// Test-only exports for the external test package (decide_test), which exists
// because the plan engines import this package: only an external test can hold
// their real prompts against what D2 recognises.
var (
	EnginePromptForTest = enginePrompt
	GoalOfForTest       = goalOf
	TitleOfForTest      = titleOf
)
