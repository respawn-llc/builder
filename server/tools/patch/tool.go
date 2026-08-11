package patch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"core/server/tools"
	"core/shared/transcript"
	patchformat "core/shared/transcript/patchformat"
)

type input struct {
	Patch string `json:"patch"`
}

type Tool struct {
	fileAccess *tools.FileAccessPolicy
}

func New(filesystemContext tools.FilesystemContext, opts ...Option) (*Tool, error) {
	settings := options{}
	for _, opt := range opts {
		if opt != nil {
			opt(&settings)
		}
	}
	fileAccess, err := tools.NewFileAccessPolicy(tools.FileAccessPolicyConfig{
		Context:               filesystemContext,
		Mode:                  tools.FileAccessMutation,
		AllowOutsideWorkspace: settings.allowOutsideWorkspace,
		Approver:              settings.outsideWorkspaceApprover,
		PathDenyPolicy:        settings.pathDenyPolicy,
	})
	if err != nil {
		return nil, err
	}
	return &Tool{fileAccess: fileAccess}, nil
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
	deletionFacts, err := t.apply(ctx, doc)
	if err != nil {
		if errors.Is(err, tools.ErrForeignManagedWorktreeEdit) {
			return tools.ErrorResult(c, tools.ForeignManagedWorktreeEditDeniedMessage), nil
		}
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
	return commitStagedFiles(t, states, state.deleteTargets)
}
