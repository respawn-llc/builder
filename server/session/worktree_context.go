package session

import (
	"errors"
	"strings"

	"github.com/google/uuid"
)

func OptionalWorktreeBranch(value string) *string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return nil
	}
	return &trimmed
}

func NormalizeWorktreeReminderState(state WorktreeReminderState) (WorktreeReminderState, error) {
	state.Mode = WorktreeReminderMode(strings.TrimSpace(string(state.Mode)))
	switch state.Mode {
	case WorktreeReminderModeEnter, WorktreeReminderModeExit:
	default:
		return WorktreeReminderState{}, errors.New("worktree reminder mode is required")
	}
	context, err := normalizeWorktreeContext(state.WorktreeContext)
	if err != nil {
		return WorktreeReminderState{}, err
	}
	if context.WorktreePath == "" {
		return WorktreeReminderState{}, errors.New("worktree reminder worktree path is required")
	}
	state.WorktreeContext = context
	return state, nil
}

func normalizeWorktreeContext(context WorktreeContext) (WorktreeContext, error) {
	context.ContextID = cloneUUID(context.ContextID)
	if context.ContextID != nil {
		if *context.ContextID == uuid.Nil || context.ContextID.Version() != 4 {
			return WorktreeContext{}, errors.New("worktree context id must be a UUID v4 when present")
		}
	}
	if context.Branch != nil {
		branch := strings.TrimSpace(*context.Branch)
		if branch == "" {
			return WorktreeContext{}, errors.New("worktree context branch must be non-empty when present")
		}
		context.Branch = &branch
	}
	context.WorktreePath = strings.TrimSpace(context.WorktreePath)
	context.WorkspaceRoot = strings.TrimSpace(context.WorkspaceRoot)
	context.EffectiveCwd = strings.TrimSpace(context.EffectiveCwd)
	if context.WorkspaceRoot == "" {
		return WorktreeContext{}, errors.New("worktree reminder workspace root is required")
	}
	if context.EffectiveCwd == "" {
		return WorktreeContext{}, errors.New("worktree reminder effective cwd is required")
	}
	return context, nil
}

func CloneWorktreeContext(context *WorktreeContext) *WorktreeContext {
	if context == nil {
		return nil
	}
	copyContext := *context
	copyContext.ContextID = cloneUUID(context.ContextID)
	if context.Branch != nil {
		branch := *context.Branch
		copyContext.Branch = &branch
	}
	return &copyContext
}

func CloneWorktreeReminderState(state *WorktreeReminderState) *WorktreeReminderState {
	if state == nil {
		return nil
	}
	copyState := *state
	copyState.WorktreeContext = *CloneWorktreeContext(&state.WorktreeContext)
	return &copyState
}

func WorktreeContextEqual(left, right WorktreeContext) bool {
	return optionalUUIDEqual(left.ContextID, right.ContextID) &&
		optionalStringEqual(left.Branch, right.Branch) &&
		left.WorktreePath == right.WorktreePath &&
		left.WorkspaceRoot == right.WorkspaceRoot &&
		left.EffectiveCwd == right.EffectiveCwd
}

func WorktreeReminderTargetEqual(left, right WorktreeReminderState) bool {
	left.ContextID = nil
	right.ContextID = nil
	return left.Mode == right.Mode && WorktreeContextEqual(left.WorktreeContext, right.WorktreeContext)
}

func WorktreeReminderStateEqual(left, right WorktreeReminderState) bool {
	return left.Mode == right.Mode && WorktreeContextEqual(left.WorktreeContext, right.WorktreeContext)
}

func optionalUUIDEqual(left, right *uuid.UUID) bool {
	if left == nil || right == nil {
		return left == right
	}
	return *left == *right
}

func optionalStringEqual(left, right *string) bool {
	if left == nil || right == nil {
		return left == right
	}
	return *left == *right
}

func cloneUUID(value *uuid.UUID) *uuid.UUID {
	if value == nil {
		return nil
	}
	copyValue := *value
	return &copyValue
}
