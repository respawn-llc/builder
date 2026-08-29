package clientui

import (
	"fmt"
	"strings"
)

type SessionSettingKind string

const (
	SessionSettingSessionName    SessionSettingKind = "session_name"
	SessionSettingThinking       SessionSettingKind = "thinking"
	SessionSettingFastMode       SessionSettingKind = "fast_mode"
	SessionSettingSupervisor     SessionSettingKind = "supervisor"
	SessionSettingQuestions      SessionSettingKind = "questions"
	SessionSettingAutoCompaction SessionSettingKind = "auto_compaction"
)

type TranscriptSessionSettingFeedback struct {
	Kind           SessionSettingKind
	Changed        bool
	SessionName    *string
	Thinking       *string
	FastMode       *bool
	Supervisor     *string
	Questions      *bool
	AutoCompaction *bool
}

func (f TranscriptSessionSettingFeedback) Validate() error {
	valueCount := 0
	for _, present := range []bool{
		f.SessionName != nil,
		f.Thinking != nil,
		f.FastMode != nil,
		f.Supervisor != nil,
		f.Questions != nil,
		f.AutoCompaction != nil,
	} {
		if present {
			valueCount++
		}
	}
	if valueCount != 1 {
		return fmt.Errorf("session setting feedback has %d resulting values, want exactly one", valueCount)
	}
	switch f.Kind {
	case SessionSettingSessionName:
		return validateSettingFeedbackString("Session Name", f.SessionName, true)
	case SessionSettingThinking:
		return validateSettingFeedbackString("Thinking", f.Thinking, false)
	case SessionSettingFastMode:
		if f.FastMode == nil {
			return fmt.Errorf("Fast Mode feedback requires a Fast Mode value")
		}
	case SessionSettingSupervisor:
		if f.Supervisor == nil {
			return fmt.Errorf("Supervisor feedback requires a Supervisor value")
		}
		switch *f.Supervisor {
		case "off", "edits", "all":
		default:
			return fmt.Errorf("invalid Supervisor feedback value %q", *f.Supervisor)
		}
	case SessionSettingQuestions:
		if f.Questions == nil {
			return fmt.Errorf("Questions feedback requires a Questions value")
		}
	case SessionSettingAutoCompaction:
		if f.AutoCompaction == nil {
			return fmt.Errorf("Auto-compaction feedback requires an Auto-compaction value")
		}
	default:
		return fmt.Errorf("invalid Session setting feedback kind %q", f.Kind)
	}
	return nil
}

func validateSettingFeedbackString(name string, value *string, allowEmpty bool) error {
	if value == nil || (!allowEmpty && strings.TrimSpace(*value) == "") {
		return fmt.Errorf("%s feedback requires a %s value", name, name)
	}
	if *value != strings.TrimSpace(*value) {
		return fmt.Errorf("%s feedback value must be normalized", name)
	}
	return nil
}
