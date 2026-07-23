package tools

import (
	"encoding/json"
	"fmt"
	"strings"
)

type ModelWarningKind string

const ModelWarningForeignManagedWorktreeEdit ModelWarningKind = "foreign_managed_worktree_edit"

type ModelWarning struct {
	Kind ModelWarningKind
	Text string
}

const foreignManagedWorktreeEditWarning = "[You reached into another Kent worktree. Prefer using `kent worktree enter <selector>` to edit in that worktree]"

func ForeignManagedWorktreeEditWarning() ModelWarning {
	return ModelWarning{Kind: ModelWarningForeignManagedWorktreeEdit, Text: foreignManagedWorktreeEditWarning}
}

func MaterializeModelWarnings(result Result) Result {
	if result.IsError || len(result.ModelWarnings) == 0 {
		return result
	}
	lines := make([]string, 0, len(result.ModelWarnings))
	for _, warning := range result.ModelWarnings {
		if strings.TrimSpace(warning.Text) != "" {
			lines = append(lines, warning.Text)
		}
	}
	if len(lines) == 0 {
		return result
	}
	warnings := strings.Join(lines, "\n")
	var object map[string]json.RawMessage
	if json.Unmarshal(result.Output, &object) == nil && object != nil {
		object["warning"], _ = json.Marshal(warnings)
		result.Output, _ = json.Marshal(object)
	} else {
		var text string
		if json.Unmarshal(result.Output, &text) == nil {
			result.Output, _ = json.Marshal(strings.TrimSpace(text) + "\n" + warnings)
		} else {
			result.Output, _ = json.Marshal(fmt.Sprintf("%s\n%s", strings.TrimSpace(string(result.Output)), warnings))
		}
	}
	if result.Summary != nil {
		summary := strings.TrimSpace(*result.Summary) + "\n" + warnings
		result.Summary = &summary
	}
	result.ModelWarnings = nil
	return result
}
