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
	"core/shared/transcript"
	patchformat "core/shared/transcript/patchformat"
)

type input struct {
	Patch string `json:"patch"`
}

type Tool struct {
	fileAccessScope              tools.FileAccessScope
	workspaceOnly                bool
	allowOutsideWorkspace        bool
	outsideWorkspaceApprover     OutsideWorkspaceApprover
	outsideWorkspaceSessionMu    sync.RWMutex
	outsideWorkspaceSessionAllow bool
	pathDenyPolicy               tools.PathDenyPolicy
	managedWorktreePathContext   *tools.ManagedWorktreePathContext
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
	t := &Tool{
		fileAccessScope: tools.FileAccessScope{
			WorkingDirectory:    tools.FilesystemRoot{LexicalPath: abs, RealPath: real, Info: rootInfo},
			ExecutionTargetRoot: tools.FilesystemRoot{LexicalPath: abs, RealPath: real, Info: rootInfo},
		},
		workspaceOnly: workspaceOnly,
	}
	for _, opt := range opts {
		if opt != nil {
			opt(t)
		}
	}
	if err := tools.ValidateFileAccessScope(t.fileAccessScope); err != nil {
		return nil, fmt.Errorf("validate file access scope: %w", err)
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
	foreignManagedWorktree, err := t.targetsForeignManagedWorktree(doc)
	if err != nil {
		return tools.ErrorResult(c, err.Error()), nil
	}
	if foreignManagedWorktree {
		return tools.ErrorResult(c, tools.ForeignManagedWorktreeEditDeniedMessage), nil
	}
	deletionFacts, err := t.apply(ctx, doc)
	if err != nil {
		return tools.ErrorResultWith(c, err.Error(), func(any) (json.RawMessage, error) {
			return json.Marshal(errorPayload(err))
		}), nil
	}

	body, _ := json.Marshal(map[string]any{
		"ok":         true,
		"operations": len(doc.Hunks),
	})
	result := tools.Result{CallID: c.ID, Name: c.Name, Output: body}
	if len(deletionFacts) > 0 {
		result.PresentationDelta = &transcript.ToolResultPresentationDelta{
			WholeFileDeletionFacts: deletionFacts,
		}
	}
	return result, nil
}

func (t *Tool) targetsForeignManagedWorktree(doc patchformat.Document) (bool, error) {
	if t.managedWorktreePathContext == nil {
		return false, nil
	}
	for _, hunk := range doc.Hunks {
		paths := make([]string, 0, 2)
		switch op := hunk.(type) {
		case patchformat.AddFile:
			paths = append(paths, op.Path)
		case patchformat.DeleteFile:
			paths = append(paths, op.Path)
		case patchformat.UpdateFile:
			paths = append(paths, op.Path)
			if strings.TrimSpace(op.MoveTo) != "" {
				paths = append(paths, op.MoveTo)
			}
		}
		for _, path := range paths {
			resolved, err := t.resolvePathTarget(path, false)
			if err != nil {
				return false, err
			}
			if t.managedWorktreePathContext.IsForeignManagedWorktreePath(resolved) {
				return true, nil
			}
		}
	}
	return false, nil
}

func (t *Tool) apply(
	ctx context.Context,
	doc patchformat.Document,
) ([]patchformat.WholeFileDeletionFact, error) {
	state := newApplyState(t, ctx)
	unlock, err := state.lockDocumentPaths(doc)
	if err != nil {
		return nil, err
	}
	defer unlock()
	for ordinal, h := range doc.Hunks {
		switch op := h.(type) {
		case patchformat.AddFile:
			if err := state.addFile(op); err != nil {
				return nil, err
			}
		case patchformat.DeleteFile:
			if err := state.deleteFile(
				op,
				patchformat.WholeFileDeletionOperationID{HunkOrdinal: ordinal},
			); err != nil {
				return nil, err
			}
		case patchformat.UpdateFile:
			if err := state.updateFile(op); err != nil {
				return nil, err
			}
		default:
			return nil, fmt.Errorf("unsupported patch hunk: %T", h)
		}
	}

	states, err := state.prepareCommitStates()
	if err != nil {
		return nil, err
	}
	defer cleanupStagedFiles(states)
	return commitStagedFiles(states, state.deleteTargets)
}
