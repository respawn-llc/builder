package clientui

import (
	"errors"
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
	values := 0
	for _, present := range []bool{
		f.SessionName != nil,
		f.Thinking != nil,
		f.FastMode != nil,
		f.Supervisor != nil,
		f.Questions != nil,
		f.AutoCompaction != nil,
	} {
		if present {
			values++
		}
	}
	if values != 1 {
		return fmt.Errorf("session setting feedback has %d resulting values, want exactly one", values)
	}
	switch f.Kind {
	case SessionSettingSessionName:
		if f.SessionName == nil {
			return errors.New("Session Name feedback requires a Session Name value")
		}
		if *f.SessionName != strings.TrimSpace(*f.SessionName) {
			return errors.New("Session Name feedback value must be normalized")
		}
	case SessionSettingThinking:
		if f.Thinking == nil || strings.TrimSpace(*f.Thinking) == "" {
			return errors.New("Thinking feedback requires a Thinking value")
		}
		if *f.Thinking != strings.TrimSpace(*f.Thinking) {
			return errors.New("Thinking feedback value must be normalized")
		}
	case SessionSettingFastMode:
		if f.FastMode == nil {
			return errors.New("Fast Mode feedback requires a Fast Mode value")
		}
	case SessionSettingSupervisor:
		if f.Supervisor == nil {
			return errors.New("Supervisor feedback requires a Supervisor value")
		}
		switch *f.Supervisor {
		case "off", "edits", "all":
		default:
			return fmt.Errorf("invalid Supervisor feedback value %q", *f.Supervisor)
		}
	case SessionSettingQuestions:
		if f.Questions == nil {
			return errors.New("Questions feedback requires a Questions value")
		}
	case SessionSettingAutoCompaction:
		if f.AutoCompaction == nil {
			return errors.New("Auto-compaction feedback requires an Auto-compaction value")
		}
	default:
		return fmt.Errorf("invalid Session setting feedback kind %q", f.Kind)
	}
	return nil
}
