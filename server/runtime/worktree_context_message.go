package runtime

import (
	"errors"
	"fmt"
	"strings"

	"core/server/llm"
	"core/server/session"
)

func normalizePersistedMessageWorktreeContext(message llm.Message) (llm.Message, error) {
	isWorktreeMessage := message.MessageType == llm.MessageTypeWorktreeMode ||
		message.MessageType == llm.MessageTypeWorktreeModeExit
	if message.WorktreeContext == nil {
		if isWorktreeMessage {
			return llm.Message{}, errors.New("persist worktree context message: typed worktree context is required")
		}
		return message, nil
	}
	if !isWorktreeMessage {
		return llm.Message{}, fmt.Errorf("persist worktree context message: message type %q cannot carry worktree context", message.MessageType)
	}
	if strings.TrimSpace(message.SourcePath) != "" {
		return llm.Message{}, errors.New("persist worktree context message: source_path duplicates typed effective cwd")
	}
	mode := session.WorktreeReminderModeEnter
	if message.MessageType == llm.MessageTypeWorktreeModeExit {
		mode = session.WorktreeReminderModeExit
	}
	state, err := session.NormalizeWorktreeReminderState(session.WorktreeReminderState{
		Mode:            mode,
		WorktreeContext: *session.CloneWorktreeContext(message.WorktreeContext),
	})
	if err != nil {
		return llm.Message{}, fmt.Errorf("persist worktree context message: %w", err)
	}
	message.WorktreeContext = session.CloneWorktreeContext(&state.WorktreeContext)
	return message, nil
}
