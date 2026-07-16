package app

import (
	"context"
	"errors"
	"strings"

	"core/shared/apicontract"
	"core/shared/clientui"
	"core/shared/serverapi"

	"github.com/google/uuid"
)

type backgroundUIProcessClient struct {
	reads   apicontract.ProcessViewService
	control apicontract.ProcessControlService
}

func newUIProcessClientWithReads(reads apicontract.ProcessViewService, control apicontract.ProcessControlService) clientui.ProcessClient {
	if reads == nil && control == nil {
		return nil
	}
	return backgroundUIProcessClient{reads: reads, control: control}
}

func (m *uiModel) listProcesses() []clientui.BackgroundProcess {
	if m == nil || m.processClient == nil {
		return nil
	}
	entries, err := m.listProcessesWithError(context.Background())
	if err != nil {
		return nil
	}
	return entries
}

func (m *uiModel) listProcessesWithError(ctx context.Context) ([]clientui.BackgroundProcess, error) {
	if m == nil || m.processClient == nil {
		return nil, nil
	}
	m.checkTUIBlockingOperation("process list read", "")
	return m.processClient.ListProcesses(ctx)
}

func (c backgroundUIProcessClient) ListProcesses(ctx context.Context) ([]clientui.BackgroundProcess, error) {
	if c.reads != nil {
		resp, err := c.reads.ListProcesses(ctx, serverapi.ProcessListRequest{})
		if err != nil {
			return nil, err
		}
		return resp.Processes, nil
	}
	return nil, nil
}

func (c backgroundUIProcessClient) KillProcess(ctx context.Context, id string) error {
	id = strings.TrimSpace(id)
	if c.control != nil {
		_, err := c.control.KillProcess(ctx, serverapi.ProcessKillRequest{ClientRequestID: uuid.NewString(), ProcessID: id})
		if err != nil {
			return err
		}
		return nil
	}
	return errors.New("process control client is unavailable")
}

func (c backgroundUIProcessClient) InlineOutput(ctx context.Context, id string, maxChars int) (string, string, error) {
	id = strings.TrimSpace(id)
	if c.control != nil {
		resp, err := c.control.GetInlineOutput(ctx, serverapi.ProcessInlineOutputRequest{ProcessID: id, MaxChars: maxChars})
		if err != nil {
			return "", "", err
		}
		return resp.Output, resp.LogPath, nil
	}
	return "", "", errors.New("process control client is unavailable")
}
