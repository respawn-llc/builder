package metadata

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"core/shared/config"
)

type WorkspaceChatDraftDocument struct {
	Message        string `json:"message"`
	Agent          string `json:"agent"`
	Supervisor     string `json:"supervisor"`
	Thinking       string `json:"thinking"`
	Fast           bool   `json:"fast"`
	Questions      bool   `json:"questions"`
	AutoCompaction bool   `json:"auto_compaction"`
}

func (d WorkspaceChatDraftDocument) Validate() error {
	agent := strings.TrimSpace(d.Agent)
	if agent == "" {
		return errors.New("workspace Chat draft agent is required")
	}
	if !strings.EqualFold(agent, config.DefaultSubagentRole) && config.NormalizeSubagentRole(agent) == "" {
		return fmt.Errorf("workspace Chat draft agent %q is invalid", d.Agent)
	}
	switch d.Supervisor {
	case "off", "edits", "all":
	default:
		return fmt.Errorf("workspace Chat draft supervisor %q is invalid", d.Supervisor)
	}
	if strings.TrimSpace(d.Thinking) == "" {
		return errors.New("workspace Chat draft thinking is required")
	}
	return nil
}

func (d *WorkspaceChatDraftDocument) UnmarshalJSON(data []byte) error {
	if d == nil {
		return errors.New("workspace Chat draft document is required")
	}
	type wire struct {
		Message        *string `json:"message"`
		Agent          *string `json:"agent"`
		Supervisor     *string `json:"supervisor"`
		Thinking       *string `json:"thinking"`
		Fast           *bool   `json:"fast"`
		Questions      *bool   `json:"questions"`
		AutoCompaction *bool   `json:"auto_compaction"`
	}
	var decoded wire
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&decoded); err != nil {
		return err
	}
	if decoded.Message == nil || decoded.Agent == nil || decoded.Supervisor == nil ||
		decoded.Thinking == nil || decoded.Fast == nil || decoded.Questions == nil ||
		decoded.AutoCompaction == nil {
		return errors.New("workspace Chat draft document is incomplete")
	}
	*d = WorkspaceChatDraftDocument{
		Message:        *decoded.Message,
		Agent:          *decoded.Agent,
		Supervisor:     *decoded.Supervisor,
		Thinking:       *decoded.Thinking,
		Fast:           *decoded.Fast,
		Questions:      *decoded.Questions,
		AutoCompaction: *decoded.AutoCompaction,
	}
	return d.Validate()
}
