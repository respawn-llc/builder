package patch

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"core/server/tools"
	patchformat "core/shared/transcript/patchformat"
)

type input struct {
	Patch string `json:"patch"`
}

type Tool struct {
	workspaceRoot                string
	workspaceRootReal            string
	workspaceRootInfo            os.FileInfo
	workspaceOnly                bool
	allowOutsideWorkspace        bool
	outsideWorkspaceApprover     OutsideWorkspaceApprover
	outsideWorkspaceSessionMu    sync.RWMutex
	outsideWorkspaceSessionAllow bool
	pathDenyPolicy               tools.PathDenyPolicy
}

func New(workspaceRoot string, workspaceOnly bool, opts ...Option) (*Tool, error) {
	abs, err := filepath.Abs(workspaceRoot)
	if err != nil {
		return nil, fmt.Errorf("resolve workspace root: %w", err)
	}
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return nil, tools.WrapMissingWorkspaceRootError(abs, fmt.Errorf("resolve workspace real path: %w", err))
	}
	rootInfo, err := os.Stat(real)
	if err != nil {
		return nil, tools.WrapMissingWorkspaceRootError(abs, fmt.Errorf("stat workspace root: %w", err))
	}
	t := &Tool{workspaceRoot: abs, workspaceRootReal: real, workspaceRootInfo: rootInfo, workspaceOnly: workspaceOnly}
	for _, opt := range opts {
		if opt != nil {
			opt(t)
		}
	}
	return t, nil
}

func (t *Tool) Call(ctx context.Context, c tools.Call) (tools.Result, error) {
	var in input
	if err := json.Unmarshal(c.Input, &in); err != nil {
		return tools.ErrorResult(c, fmt.Sprintf("invalid input: %v", err)), nil
	}
	if strings.TrimSpace(in.Patch) == "" {
		return tools.ErrorResult(c, "patch is required"), nil
	}

	doc, err := patchformat.Parse(in.Patch)
	if err != nil {
		patchErr := malformedFailure(err.Error())
		return tools.ErrorResultWith(c, patchErr.Error(), func(any) (json.RawMessage, error) {
			return json.Marshal(errorPayload(patchErr))
		}), nil
	}
	if len(doc.Hunks) == 0 {
		patchErr := malformedFailure("No files were modified.")
		return tools.ErrorResultWith(c, patchErr.Error(), func(any) (json.RawMessage, error) {
			return json.Marshal(errorPayload(patchErr))
		}), nil
	}
	if err := t.apply(ctx, doc); err != nil {
		return tools.ErrorResultWith(c, err.Error(), func(any) (json.RawMessage, error) {
			return json.Marshal(errorPayload(err))
		}), nil
	}

	body, _ := json.Marshal(map[string]any{
		"ok":         true,
		"operations": len(doc.Hunks),
	})
	return tools.Result{CallID: c.ID, Name: c.Name, Output: body}, nil
}

func (t *Tool) apply(ctx context.Context, doc patchformat.Document) error {
	state := newApplyState(t, ctx)
	unlock, err := state.lockDocumentPaths(doc)
	if err != nil {
		return err
	}
	defer unlock()
	for _, h := range doc.Hunks {
		switch op := h.(type) {
		case patchformat.AddFile:
			if err := state.addFile(op); err != nil {
				return err
			}
		case patchformat.DeleteFile:
			if err := state.deleteFile(op); err != nil {
				return err
			}
		case patchformat.UpdateFile:
			if err := state.updateFile(op); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unsupported patch hunk: %T", h)
		}
	}

	states, err := state.prepareCommitStates()
	if err != nil {
		return err
	}
	defer cleanupStagedFiles(states)
	return commitStagedFiles(states, state.deleteTargets)
}
