package session

import (
	"errors"
	"fmt"
	"strings"

	"core/shared/serverapi"
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
	return SessionRebindReminder{
		SourceProject:    source,
		TargetProject:    target,
		WorkingDirectory: workingDirectory,
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
	return &clone
}

func SessionRebindReminderEqual(left, right SessionRebindReminder) bool {
	if left.SourceProject != right.SourceProject || left.TargetProject != right.TargetProject {
		return false
	}
	if left.WorkingDirectory == nil || right.WorkingDirectory == nil {
		return left.WorkingDirectory == nil && right.WorkingDirectory == nil
	}
	return *left.WorkingDirectory == *right.WorkingDirectory
}
