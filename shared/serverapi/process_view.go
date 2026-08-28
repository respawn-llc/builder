package serverapi

import (
	"errors"
	"strings"

	"core/shared/clientui"
)

type ProcessListRequest struct {
	OwnerSessionID string
	OwnerRunID     string
}

type ProcessListResponse struct {
	Processes []clientui.BackgroundProcess
}

type ProcessGetRequest struct {
	ProcessID string
}

type ProcessGetResponse struct {
	Process *clientui.BackgroundProcess
}

type ProcessKillRequest struct {
	ProcessID string
}

type ProcessKillResponse struct{}

type ProcessInlineOutputRequest struct {
	ProcessID string
	MaxChars  int
}

type ProcessInlineOutputResponse struct {
	Output  string
	LogPath string
}

func (r ProcessGetRequest) Validate() error {
	if strings.TrimSpace(r.ProcessID) == "" {
		return errors.New("process_id is required")
	}
	return nil
}

func (r ProcessKillRequest) Validate() error {
	if strings.TrimSpace(r.ProcessID) == "" {
		return errors.New("process_id is required")
	}
	return nil
}

func (r ProcessInlineOutputRequest) Validate() error {
	if strings.TrimSpace(r.ProcessID) == "" {
		return errors.New("process_id is required")
	}
	return nil
}
