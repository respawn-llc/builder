package runtimefeed

import (
	"fmt"
	"strings"

	"core/shared/runtimeids"
)

type ProcessID string

type BackgroundLifecycle string

const (
	BackgroundLifecycleBackgrounded BackgroundLifecycle = "backgrounded"
	BackgroundLifecycleCompleted    BackgroundLifecycle = "completed"
	BackgroundLifecycleKilled       BackgroundLifecycle = "killed"
)

type TranscriptBackgroundActivity struct {
	ActivityID        runtimeids.BackgroundActivityID
	ProcessID         ProcessID
	OwnerRunID        runtimeids.RunID
	OwnerStepID       runtimeids.StepID
	Lifecycle         BackgroundLifecycle
	Command           string
	Workdir           string
	LogPath           *string
	Preview           *string
	ExitCode          *int
	UserRequestedKill bool
	NoticeSuppressed  bool
	Diagnostic        *TranscriptDiagnostic
}

func (a TranscriptBackgroundActivity) Validate() error {
	if a.ActivityID.IsZero() {
		return fmt.Errorf("background activity id is required")
	}
	if strings.TrimSpace(string(a.ProcessID)) == "" {
		return fmt.Errorf("background process id is required")
	}
	if a.OwnerRunID.IsZero() {
		return fmt.Errorf("background owner run id is required")
	}
	if a.OwnerStepID.IsZero() {
		return fmt.Errorf("background owner step id is required")
	}
	if strings.TrimSpace(a.Command) == "" {
		return fmt.Errorf("background command is required")
	}
	if strings.TrimSpace(a.Workdir) == "" {
		return fmt.Errorf("background workdir is required")
	}
	if err := validateOptionalNonEmptyString("background log path", a.LogPath); err != nil {
		return err
	}
	if err := validateOptionalNonEmptyString("background preview", a.Preview); err != nil {
		return err
	}
	switch a.Lifecycle {
	case BackgroundLifecycleBackgrounded:
		if a.ExitCode != nil {
			return fmt.Errorf("backgrounded activity cannot carry exit code")
		}
		if a.UserRequestedKill {
			return fmt.Errorf("backgrounded activity cannot be user-killed")
		}
		if a.Diagnostic != nil {
			return fmt.Errorf("backgrounded activity cannot carry terminal diagnostic")
		}
		return nil
	case BackgroundLifecycleCompleted:
		if a.UserRequestedKill {
			return fmt.Errorf("completed background activity cannot be user-killed")
		}
	case BackgroundLifecycleKilled:
	default:
		return fmt.Errorf("unknown background lifecycle %q", a.Lifecycle)
	}
	if a.Diagnostic != nil {
		return a.Diagnostic.Validate()
	}
	return nil
}

func validateOptionalNonEmptyString(owner string, value *string) error {
	if value != nil && strings.TrimSpace(*value) == "" {
		return fmt.Errorf("%s cannot be empty when present", owner)
	}
	return nil
}
