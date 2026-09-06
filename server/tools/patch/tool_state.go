package patch

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"core/server/tools"
	patchformat "core/shared/transcript/patchformat"
)

type applyState struct {
	tool          *Tool
	ctx           context.Context
	state         map[string]*patchFileState
	deleteTargets map[string]wholeFileDeletionTarget
	accessCall    *tools.FileAccessCall
}

type wholeFileDeletionTarget struct {
	OperationIDs []patchformat.WholeFileDeletionOperationID
}

func newApplyState(tool *Tool, ctx context.Context) *applyState {
	return &applyState{
		tool:          tool,
		ctx:           ctx,
		state:         map[string]*patchFileState{},
		deleteTargets: map[string]wholeFileDeletionTarget{},
		accessCall:    tool.fileAccess.BeginCall(),
	}
}

func (s *applyState) hasDeletedAncestor(path string) bool {
	for current := filepath.Dir(path); current != "" && current != path; current = filepath.Dir(current) {
		if _, ok := s.deleteTargets[current]; ok {
			return true
		}
		next := filepath.Dir(current)
		if next == current {
			break
		}
	}
	return false
}

func (s *applyState) lockDocumentPaths(doc patchformat.Document) (func(), error) {
	type documentPath struct {
		raw       string
		resolved  string
		mustExist bool
	}
	targets := make([]documentPath, 0, len(doc.Hunks))
	addPath := func(raw string, mustExist bool) error {
		if strings.TrimSpace(raw) == "" {
			return nil
		}
		resolved, err := s.tool.resolvePathTarget(raw, false)
		if err != nil {
			return err
		}
		outcome := s.tool.fileAccess.CheckMutationTarget(raw, resolved)
		if outcome.Kind != tools.FileAccessTargetAccepted {
			return fileAccessFailure(outcome)
		}
		if mustExist {
			resolved, err = s.tool.resolvePathTarget(raw, true)
			if err != nil {
				return err
			}
		}
		targets = append(targets, documentPath{raw: raw, resolved: resolved, mustExist: mustExist})
		return nil
	}
	for _, hunk := range doc.Hunks {
		switch op := hunk.(type) {
		case patchformat.AddFile:
			if err := addPath(op.Path, false); err != nil {
				return nil, err
			}
		case patchformat.DeleteFile:
			if err := addPath(op.Path, true); err != nil {
				return nil, err
			}
		case patchformat.UpdateFile:
			if err := addPath(op.Path, false); err != nil {
				return nil, err
			}
			if strings.TrimSpace(op.MoveTo) != "" {
				if err := addPath(op.MoveTo, false); err != nil {
					return nil, err
				}
			}
		}
	}
	accessTargets := make([]tools.FileAccessTarget, 0, len(targets))
	for _, target := range targets {
		accessTargets = append(accessTargets, tools.FileAccessTarget{
			RequestedPath: target.raw,
			ResolvedPath:  target.resolved,
		})
	}
	if outcome := s.accessCall.Prepare(s.ctx, accessTargets); !outcome.IsAllowed() {
		return nil, fileAccessFailure(outcome)
	}

	paths := make([]string, 0, len(targets))
	for _, target := range targets {
		current, err := s.tool.resolvePathTarget(target.raw, target.mustExist)
		if err != nil {
			return nil, err
		}
		outcome := s.accessCall.Authorize(s.ctx, target.raw, current)
		if !outcome.IsAllowed() {
			return nil, fileAccessFailure(outcome)
		}
		paths = append(paths, current)
	}
	return tools.LockFileAccessPaths(paths), nil
}

func (s *applyState) getState(path string) (*patchFileState, error) {
	resolved, err := s.tool.resolvePath(s.ctx, path, false, s.accessCall)
	if err != nil {
		return nil, err
	}
	if existing, ok := s.state[resolved]; ok {
		return existing, nil
	}
	fileState := &patchFileState{Mode: 0o644, NewPath: resolved, Original: resolved}
	snapshot, err := captureSnapshot(resolved)
	if err == nil && snapshot.Exists {
		fileState.Exists = true
		fileState.Content = splitLines(string(snapshot.Data))
		fileState.Mode = snapshot.Mode
	} else if err != nil {
		return nil, internalFailure(path, fmt.Sprintf("read file failed: %v", err))
	}
	s.state[resolved] = fileState
	return fileState, nil
}

func (s *applyState) addFile(op patchformat.AddFile) error {
	target, err := s.tool.resolvePath(s.ctx, op.Path, false, s.accessCall)
	if err != nil {
		return err
	}
	if _, exists := s.state[target]; exists {
		return targetExistsFailure(op.Path, "patch already referenced this path earlier in the same patch")
	}
	_, allowReplacement := s.deleteTargets[target]
	allowBlockedAncestor := s.hasDeletedAncestor(target)
	if _, err := os.Stat(target); err == nil {
		if !allowReplacement {
			return targetExistsFailure(op.Path, "cannot add a file over an existing path")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		if !allowReplacement && !allowBlockedAncestor {
			return internalFailure(op.Path, fmt.Sprintf("stat add target failed: %v", err))
		}
	}
	s.state[target] = &patchFileState{
		Exists:   true,
		Content:  append([]string(nil), op.Content...),
		Mode:     0o644,
		NewPath:  target,
		Original: target,
	}
	return nil
}

func (s *applyState) deleteFile(
	op patchformat.DeleteFile,
	operationID patchformat.WholeFileDeletionOperationID,
) error {
	target, err := s.tool.resolvePath(s.ctx, op.Path, true, s.accessCall)
	if err != nil {
		return err
	}
	if _, exists := s.state[target]; exists {
		return malformedFailure(fmt.Sprintf("delete target already referenced: %s", op.Path))
	}
	snapshot, err := captureSnapshot(target)
	if err != nil {
		return internalFailure(op.Path, fmt.Sprintf("stat delete target failed: %v", err))
	}
	if !snapshot.Exists {
		return targetMissingFailure(op.Path, "cannot delete a file that does not exist")
	}
	deleteTarget := s.deleteTargets[target]
	deleteTarget.OperationIDs = append(deleteTarget.OperationIDs, operationID)
	s.deleteTargets[target] = deleteTarget
	return nil
}

func (s *applyState) updateFile(op patchformat.UpdateFile) error {
	resolved, err := s.tool.resolvePath(s.ctx, op.Path, false, s.accessCall)
	if err != nil {
		return err
	}
	if _, ok := s.deleteTargets[resolved]; ok {
		return malformedFailure(fmt.Sprintf("update target already marked for deletion: %s", op.Path))
	}
	fileState, err := s.getState(op.Path)
	if err != nil {
		return err
	}
	if !fileState.Exists {
		return targetMissingFailure(op.Path, "cannot update a file that does not exist")
	}
	updated, err := applyEdit(fileState.Content, op.Changes)
	if err != nil {
		return attachFailurePath(err, op.Path)
	}
	fileState.Content = updated
	if strings.TrimSpace(op.MoveTo) == "" {
		return nil
	}
	moveTarget, err := s.tool.resolvePath(s.ctx, op.MoveTo, false, s.accessCall)
	if err != nil {
		return err
	}
	if moveTarget == fileState.Original {
		return nil
	}
	if _, ok := s.state[moveTarget]; ok {
		return targetExistsFailure(op.MoveTo, "patch already referenced the move destination earlier in the same patch")
	}
	_, allowReplacement := s.deleteTargets[moveTarget]
	allowBlockedAncestor := s.hasDeletedAncestor(moveTarget)
	if _, err := os.Stat(moveTarget); err == nil {
		if !allowReplacement {
			return targetExistsFailure(op.MoveTo, "cannot move onto an existing path")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		if !allowReplacement && !allowBlockedAncestor {
			return internalFailure(op.MoveTo, fmt.Sprintf("stat move target failed: %v", err))
		}
	}
	delete(s.state, fileState.NewPath)
	fileState.NewPath = moveTarget
	s.state[moveTarget] = fileState
	return nil
}

func (s *applyState) prepareCommitStates() ([]*patchFileState, error) {
	states := sortedCommitStates(s.state)
	for _, fileState := range states {
		if err := revalidateCommitPath(s.tool, fileState.NewPath); err != nil {
			cleanupStagedFiles(states)
			return nil, err
		}
		text := strings.Join(fileState.Content, "\n")
		if len(fileState.Content) > 0 && !strings.HasSuffix(text, "\n") {
			text += "\n"
		}
		staged, err := createStagedFile(fileState.NewPath, []byte(text), fileState.Mode)
		if err != nil {
			cleanupStagedFiles(states)
			return nil, internalFailure(fileState.NewPath, fmt.Sprintf("stage write failed: %v", err))
		}
		fileState.StagedPath = staged
	}
	return states, nil
}
