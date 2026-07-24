package routines

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Step kinds.
const (
	StepCommand    = "command"
	StepAIPrompt   = "ai-prompt"
	StepCreateTask = "create-task"
)

// Step is one typed unit of work in a routine. The three kinds share this flat
// shape (only the relevant fields are set); it round-trips through the routines
// row's `steps` JSON array. Kept flat (not a sum type) so the storage JSON and
// the web builder stay simple — the executor switches on Type.
type Step struct {
	Type string `json:"type"`
	Name string `json:"name"`

	// command
	Command           string `json:"command,omitempty"`
	TimeoutSec        int    `json:"timeoutSec,omitempty"` // per-step override (command|ai-prompt)
	ContinueOnFailure bool   `json:"continueOnFailure,omitempty"`

	// ai-prompt
	Prompt string `json:"prompt,omitempty"`
	Model  string `json:"model,omitempty"`

	// create-task
	TaskTitle   string `json:"taskTitle,omitempty"`
	TaskPrompt  string `json:"taskPrompt,omitempty"`
	BoardColumn string `json:"boardColumn,omitempty"`
}

// StepResult is the per-step outcome recorded in routine_runs.detail.
type StepResult struct {
	Name   string `json:"name"`
	Type   string `json:"type"`
	Status string `json:"status"` // ok|failed|timeout|skipped
	// Output is a truncated tail of stdout (command/ai-prompt) or a short note
	// (create-task → the created card id). Bounded so detail stays under 8KB.
	Output string `json:"output,omitempty"`
	Error  string `json:"error,omitempty"`
}

// ValidateSteps parses and validates a steps JSON array: at least one step, a
// known Type per step, and the kind's required field present. Returns the
// decoded slice so the caller can re-marshal the canonical form. Pure;
// unit-tested. Empty/blank input is an error (a routine with no steps is
// meaningless).
func ValidateSteps(raw string) ([]Step, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, fmt.Errorf("steps: at least one step is required")
	}
	var steps []Step
	if err := json.Unmarshal([]byte(raw), &steps); err != nil {
		return nil, fmt.Errorf("steps: invalid JSON: %w", err)
	}
	return validateStepSlice(steps)
}

// ValidateStepSlice validates an already-decoded slice (the API path decodes the
// whole body once, then hands the slice here). Pure; unit-tested.
func ValidateStepSlice(steps []Step) ([]Step, error) { return validateStepSlice(steps) }

func validateStepSlice(steps []Step) ([]Step, error) {
	if len(steps) == 0 {
		return nil, fmt.Errorf("steps: at least one step is required")
	}
	for i, s := range steps {
		switch s.Type {
		case StepCommand:
			if strings.TrimSpace(s.Command) == "" {
				return nil, fmt.Errorf("steps[%d] (command): command is required", i)
			}
		case StepAIPrompt:
			if strings.TrimSpace(s.Prompt) == "" {
				return nil, fmt.Errorf("steps[%d] (ai-prompt): prompt is required", i)
			}
		case StepCreateTask:
			if strings.TrimSpace(s.TaskTitle) == "" || strings.TrimSpace(s.TaskPrompt) == "" {
				return nil, fmt.Errorf("steps[%d] (create-task): taskTitle and taskPrompt are required", i)
			}
		default:
			return nil, fmt.Errorf("steps[%d]: unknown type %q (want command|ai-prompt|create-task)", i, s.Type)
		}
	}
	return steps, nil
}

// MarshalSteps renders the canonical steps JSON for storage. Pure.
func MarshalSteps(steps []Step) (string, error) {
	b, err := json.Marshal(steps)
	if err != nil {
		return "", err
	}
	return string(b), nil
}
