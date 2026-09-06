package clientui

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"core/shared/runtimeids"
	"core/shared/sessioncontract"
)

type ApprovalDecision = sessioncontract.PromptApprovalDecision

const (
	ApprovalDecisionAllowOnce    = sessioncontract.PromptApprovalDecisionAllowOnce
	ApprovalDecisionAllowSession = sessioncontract.PromptApprovalDecisionAllowSession
	ApprovalDecisionDeny         = sessioncontract.PromptApprovalDecisionDeny
)

type ApprovalOption struct {
	Decision ApprovalDecision
	Label    string
}

type FileAccessTarget struct {
	RequestedPath string `json:"requested_path"`
	ResolvedPath  string `json:"resolved_path"`
}

func (t FileAccessTarget) Validate() error {
	if strings.TrimSpace(t.RequestedPath) == "" {
		return errors.New("requested path is required")
	}
	if strings.TrimSpace(t.ResolvedPath) == "" {
		return errors.New("resolved path is required")
	}
	return nil
}

func FormatFileAccessApprovalMarkdown(targets []FileAccessTarget) string {
	var formatted strings.Builder
	fmt.Fprintf(&formatted, "Agent wants to access a batch of files, but %d are outside workspace dir:", len(targets))
	formatted.WriteByte('\n')
	for _, target := range targets {
		fmt.Fprintf(&formatted, "\n- %s", target.RequestedPath)
		if target.RequestedPath != target.ResolvedPath {
			fmt.Fprintf(&formatted, " → %s", target.ResolvedPath)
		}
	}
	formatted.WriteString("\n\nAllow this access?")
	return formatted.String()
}

type PendingApproval struct {
	ToolCallID    ToolCallID
	SessionID     runtimeids.SessionID
	StepID        runtimeids.StepID
	Question      string
	Options       []ApprovalOption
	AccessTargets []FileAccessTarget
	CreatedAt     time.Time
}
