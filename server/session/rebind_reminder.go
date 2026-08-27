package session

import (
	"errors"
	"fmt"
	"strings"

	"core/shared/serverapi"
	"core/shared/textutil"
)

func NormalizeSessionRebindReminder(reminder SessionRebindReminder) (SessionRebindReminder, error) {
	source, err := normalizeProjectReference(reminder.SourceProject)
	if err != nil {
		return SessionRebindReminder{}, fmt.Errorf("source project: %w", err)
	}
	target, err := normalizeProjectReference(reminder.TargetProject)
	if err != nil {
		return SessionRebindReminder{}, fmt.Errorf("target project: %w", err)
	}
	var workingDirectory *string
	if reminder.WorkingDirectory != nil {
		value := strings.TrimSpace(*reminder.WorkingDirectory)
		if value == "" {
			return SessionRebindReminder{}, errors.New("working directory must be non-empty when present")
		}
		workingDirectory = &value
	}
	var failureDiagnostic *string
	if reminder.FailureDiagnostic != nil {
		value := strings.TrimSpace(*reminder.FailureDiagnostic)
		if value == "" {
			return SessionRebindReminder{}, errors.New("failure diagnostic must be non-empty when present")
		}
		failureDiagnostic = &value
	}
	switch reminder.Kind {
	case SessionRebindReminderSucceeded:
		if failureDiagnostic != nil {
			return SessionRebindReminder{}, errors.New("successful rebind reminder cannot contain a failure diagnostic")
		}
	case SessionRebindReminderFailed:
		if failureDiagnostic == nil || workingDirectory == nil {
			return SessionRebindReminder{}, errors.New("failed rebind reminder requires a diagnostic and unchanged working directory")
		}
	default:
		return SessionRebindReminder{}, errors.New("rebind reminder kind is required")
	}
	return SessionRebindReminder{
		Kind:              reminder.Kind,
		SourceProject:     source,
		TargetProject:     target,
		WorkingDirectory:  workingDirectory,
		FailureDiagnostic: failureDiagnostic,
	}, nil
}

func normalizeProjectReference(reference serverapi.ProjectReference) (serverapi.ProjectReference, error) {
	reference.ID = strings.TrimSpace(reference.ID)
	reference.Name = strings.TrimSpace(reference.Name)
	if reference.ID == "" {
		return serverapi.ProjectReference{}, errors.New("project id is required")
	}
	if reference.Name == "" {
		return serverapi.ProjectReference{}, errors.New("project name is required")
	}
	return reference, nil
}

func CloneSessionRebindReminder(reminder *SessionRebindReminder) *SessionRebindReminder {
	if reminder == nil {
		return nil
	}
	clone := *reminder
	if reminder.WorkingDirectory != nil {
		workingDirectory := *reminder.WorkingDirectory
		clone.WorkingDirectory = &workingDirectory
	}
	if reminder.FailureDiagnostic != nil {
		diagnostic := *reminder.FailureDiagnostic
		clone.FailureDiagnostic = &diagnostic
	}
	return &clone
}

func SessionRebindReminderEqual(left, right SessionRebindReminder) bool {
	if left.Kind != right.Kind || left.SourceProject != right.SourceProject || left.TargetProject != right.TargetProject {
		return false
	}
	if !textutil.EqualOptional(left.WorkingDirectory, right.WorkingDirectory) {
		return false
	}
	return textutil.EqualOptional(left.FailureDiagnostic, right.FailureDiagnostic)
}
