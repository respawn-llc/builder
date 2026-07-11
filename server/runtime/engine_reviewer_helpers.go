package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"core/server/llm"
	"core/server/session"
	"core/shared/transcript"
)

type reviewerSuggestionsResult struct {
	Suggestions           []string
	CacheHitPercent       int
	HasCacheHitPercentage bool
}

type reviewerRequestConfig struct {
	Model             string
	ThinkingLevel     string
	ModelCapabilities session.LockedModelCapabilities
}

func (e *Engine) runReviewerSuggestions(ctx context.Context, stepID string, reviewerClient llm.Client) (reviewerSuggestionsResult, error) {
	e.ensureOrchestrationCollaborators()
	return e.reviewerFlow.RunSuggestions(ctx, stepID, reviewerClient)
}

func parseReviewerSuggestionsObject(content string) []string {
	trimmed := strings.TrimSpace(content)
	if trimmed == "" {
		return nil
	}

	var payload struct {
		Suggestions []string `json:"suggestions"`
	}
	if err := json.Unmarshal([]byte(trimmed), &payload); err != nil {
		return nil
	}
	return sanitizeReviewerSuggestions(payload.Suggestions)
}

func sanitizeReviewerSuggestions(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	out := make([]string, 0, len(in))
	for _, suggestion := range in {
		trimmed := strings.TrimSpace(suggestion)
		if trimmed == "" {
			continue
		}
		if strings.EqualFold(trimmed, reviewerNoopToken) {
			continue
		}
		out = append(out, trimmed)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func buildReviewerRequestMessagesWithBuilder(messages []llm.Message, builder metaContextBuilder, headless bool) ([]llm.Message, error) {
	metaMessages, transcriptSource := splitMetaContextMessages(messages)
	metaMessages = filterReviewerMetaMessages(metaMessages)
	metaResult, err := builder.Build(metaContextBuildOptions{
		ExistingMessages:          metaMessages,
		IncludeAgents:             true,
		IncludeSkills:             true,
		IncludeEnvironment:        true,
		IncludeHeadless:           headless,
		PermissiveAgentsReadError: true,
	})
	if err != nil {
		return nil, err
	}
	metaMessages = metaResult.OrderedMetaMessages()
	out := make([]llm.Message, 0, len(metaMessages)+2+len(transcriptSource))
	out = append(out, metaMessages...)
	out = append(out, llm.Message{Role: llm.RoleDeveloper, Content: reviewerMetaBoundaryMessage})
	out = append(out, buildReviewerTranscriptMessages(transcriptSource)...)
	return out, nil
}

func buildReviewerRequestItemsWithBuilder(items []llm.ResponseItem, builder metaContextBuilder, headless bool) ([]llm.ResponseItem, error) {
	metaMessages, transcriptSource := splitMetaContextItems(items)
	metaMessages = filterReviewerMetaMessages(metaMessages)
	metaResult, err := builder.Build(metaContextBuildOptions{
		ExistingMessages:          metaMessages,
		IncludeAgents:             true,
		IncludeSkills:             true,
		IncludeEnvironment:        true,
		IncludeHeadless:           headless,
		PermissiveAgentsReadError: true,
	})
	if err != nil {
		return nil, err
	}
	out := reviewerItemsFromMessages(metaResult.OrderedMetaMessages())
	out = append(out, reviewerItemsFromMessages([]llm.Message{{Role: llm.RoleDeveloper, Content: reviewerMetaBoundaryMessage}})...)
	out = append(out, buildReviewerTranscriptItems(transcriptSource)...)
	return out, nil
}

func splitMetaContextItems(items []llm.ResponseItem) ([]llm.Message, []llm.ResponseItem) {
	meta := make([]llm.Message, 0, 4)
	transcriptItems := make([]llm.ResponseItem, 0, len(items))
	for _, item := range items {
		if msg, ok := messageFromResponseItem(item); ok {
			if _, classified := classifyMetaContextMessage(msg); classified {
				meta = append(meta, msg)
				continue
			}
		}
		transcriptItems = append(transcriptItems, item)
	}
	return meta, transcriptItems
}

func messageFromResponseItem(item llm.ResponseItem) (llm.Message, bool) {
	if item.Type != llm.ResponseItemTypeMessage {
		return llm.Message{}, false
	}
	role := item.Role
	if role == "" {
		role = llm.RoleUser
	}
	return llm.Message{
		Role:           role,
		MessageType:    item.MessageType,
		SourcePath:     item.SourcePath,
		Phase:          item.Phase,
		Content:        item.Content,
		CompactContent: item.CompactContent,
		Name:           item.Name,
	}, true
}

func buildReviewerTranscriptMessages(messages []llm.Message) []llm.Message {
	out := make([]llm.Message, 0, len(messages)+1)
	for _, message := range messages {
		out = append(out, reviewerTranscriptMessagesFromMessage(message)...)
	}
	if len(out) == 0 {
		out = append(out, llm.Message{Role: llm.RoleUser, Content: "No reviewable transcript entries were available for this turn."})
	}
	return out
}

func buildReviewerTranscriptItems(items []llm.ResponseItem) []llm.ResponseItem {
	transcriptMessages := make([]llm.Message, 0, len(items))
	walker := newResponseItemMessageWalker(func(msg llm.Message) {
		transcriptMessages = append(transcriptMessages, msg)
	})
	for _, item := range items {
		walker.Apply(item)
	}
	walker.Flush()
	return reviewerItemsFromMessages(buildReviewerTranscriptMessages(transcriptMessages))
}

func reviewerTranscriptMessagesFromMessage(message llm.Message) []llm.Message {
	if message.Role == llm.RoleDeveloper {
		content := strings.TrimSpace(message.Content)
		if content == "" {
			return nil
		}
		if _, ok := classifyMetaContextMessage(message); ok {
			return nil
		}
		if message.MessageType == llm.MessageTypeErrorFeedback || message.MessageType == llm.MessageTypeInterruption {
			return nil
		}
		visibleEntries := VisibleChatEntriesFromMessage(message)
		if len(visibleEntries) == 0 {
			return []llm.Message{{Role: llm.RoleUser, Content: formatReviewerHiddenDeveloperMessage(message)}}
		}
		return reviewerMessagesFromChatEntries(visibleEntries)
	}
	if message.Role == llm.RoleTool && strings.TrimSpace(message.ToolCallID) == "" {
		return nil
	}
	if strings.TrimSpace(message.Content) == "" && len(message.ToolCalls) == 0 {
		return nil
	}
	return reviewerMessagesFromChatEntries(VisibleChatEntriesFromMessage(message))
}

func reviewerItemsFromMessages(messages []llm.Message) []llm.ResponseItem {
	if len(messages) == 0 {
		return nil
	}
	items := make([]llm.ResponseItem, 0, len(messages))
	for _, msg := range messages {
		if strings.TrimSpace(msg.Content) == "" {
			continue
		}
		items = append(items, llm.PrepareOpenAIInputItems([]llm.ResponseItem{{
			Type:           llm.ResponseItemTypeMessage,
			Role:           msg.Role,
			MessageType:    msg.MessageType,
			SourcePath:     msg.SourcePath,
			Phase:          msg.Phase,
			Content:        msg.Content,
			CompactContent: msg.CompactContent,
			Name:           msg.Name,
		}})...)
	}
	return items
}

func reviewerMessagesFromChatEntries(entries []ChatEntry) []llm.Message {
	out := make([]llm.Message, 0, len(entries))
	for _, entry := range entries {
		formatted := formatReviewerChatEntry(entry)
		if strings.TrimSpace(formatted) == "" {
			continue
		}
		out = append(out, llm.Message{Role: llm.RoleUser, Content: formatted})
	}
	return out
}

func formatReviewerHiddenDeveloperMessage(message llm.Message) string {
	content := strings.TrimSpace(message.Content)
	if content == "" {
		return ""
	}
	return "Developer:\n" + content
}

func formatReviewerChatEntry(entry ChatEntry) string {
	label := reviewerChatEntryLabel(entry)
	text := strings.TrimSpace(reviewerChatEntryText(entry))
	if text == "" {
		return strings.TrimSpace(label)
	}
	return strings.TrimSpace(label) + "\n" + text
}

func reviewerChatEntryText(entry ChatEntry) string {
	if entry.Role == "tool_call" {
		if text := reviewerToolPresentationText(entry.ToolCall); text != "" {
			return text
		}
	}
	return strings.TrimSpace(entry.Text)
}

func reviewerChatEntryLabel(entry ChatEntry) string {
	switch entry.Role {
	case "assistant":
		return "Agent:"
	case "user":
		return "User:"
	case "tool_call":
		return "Tool call:"
	case "tool_result_ok":
		return "Tool result:"
	case "tool_result_error":
		return "Tool result error:"
	case string(transcript.EntryRoleCompactionSummary):
		return "Compaction:"
	case string(transcript.EntryRoleManualCompactionCarryover):
		return "Carryover:"
	case "warning":
		return "Warning:"
	case "system":
		return "System:"
	default:
		role := strings.ReplaceAll(strings.TrimSpace(entry.Role), "_", " ")
		if role == "" {
			return "Unknown:"
		}
		return fmt.Sprintf("%s:", titleCaseASCII(role))
	}
}

func reviewerToolPresentationText(meta *transcript.ToolCallMeta) string {
	if meta == nil {
		return ""
	}
	if meta.UsesAskQuestionRendering() {
		lines := make([]string, 0, len(meta.Suggestions)+2)
		if question := strings.TrimSpace(meta.Question); question != "" {
			lines = append(lines, "question: "+question)
		}
		for _, suggestion := range meta.Suggestions {
			trimmed := strings.TrimSpace(suggestion)
			if trimmed == "" {
				continue
			}
			lines = append(lines, "suggestion: "+trimmed)
		}
		if meta.RecommendedOptionIndex > 0 {
			lines = append(lines, fmt.Sprintf("recommended_option_index: %d", meta.RecommendedOptionIndex))
		}
		return strings.Join(lines, "\n")
	}
	lines := make([]string, 0, 4)
	if command := strings.TrimSpace(meta.Command); command != "" {
		lines = append(lines, command)
	} else if compact := strings.TrimSpace(meta.CompactText); compact != "" {
		lines = append(lines, compact)
	}
	if inlineMeta := strings.TrimSpace(meta.InlineMeta); inlineMeta != "" {
		lines = append(lines, "meta: "+inlineMeta)
	}
	if detail := strings.TrimSpace(meta.PatchDetail); detail != "" {
		lines = append(lines, detail)
	}
	if len(lines) == 0 {
		return strings.TrimSpace(meta.ToolName)
	}
	return strings.Join(lines, "\n")
}

func titleCaseASCII(input string) string {
	trimmed := strings.TrimSpace(input)
	if trimmed == "" {
		return "Unknown"
	}
	runes := []rune(trimmed)
	if len(runes) == 1 {
		return strings.ToUpper(trimmed)
	}
	return strings.ToUpper(string(runes[0])) + string(runes[1:])
}

func formatReviewerDeveloperInstruction(suggestions []string) string {
	b := strings.Builder{}
	b.WriteString("Supervisor agent gave you suggestions:\n")
	for idx, suggestion := range suggestions {
		b.WriteString(strconv.Itoa(idx + 1))
		b.WriteString(". ")
		b.WriteString(suggestion)
		b.WriteString("\n")
	}
	b.WriteString("\nIf no suggestions are applicable and you don't want to say anything to the user (not the supervisor!), respond with exactly ")
	b.WriteString(reviewerNoopToken)
	b.WriteString(" and no additional text. Otherwise, address the suggestions now. The supervisor can't hear you, your response will be to the user.")
	return b.String()
}

func reviewerStatusText(status ReviewerStatus, _ []string) string {
	statusText := ""
	suggestionCountLabel := pluralizeEnglish(status.SuggestionsCount, "suggestion", "suggestions")
	switch strings.TrimSpace(status.Outcome) {
	case "failed":
		if strings.TrimSpace(status.Error) == "" {
			statusText = "Supervisor ran: failed to generate suggestions."
			break
		}
		statusText = fmt.Sprintf("Supervisor ran: failed to generate suggestions: %s", status.Error)
	case "no_suggestions":
		statusText = "Supervisor ran: no suggestions."
	case "followup_failed":
		if strings.TrimSpace(status.Error) == "" {
			statusText = fmt.Sprintf("Supervisor ran: %s, but follow-up failed.", suggestionCountLabel)
			break
		}
		statusText = fmt.Sprintf("Supervisor ran: %s, but follow-up failed: %s", suggestionCountLabel, status.Error)
	case "noop":
		statusText = fmt.Sprintf("Supervisor ran: %s, no changes applied.", suggestionCountLabel)
	case "applied":
		statusText = fmt.Sprintf("Supervisor ran: %s, applied.", suggestionCountLabel)
	default:
		statusText = "Supervisor ran."
	}
	if status.HasCacheHitPercentage {
		return statusText + "\n\n" + fmt.Sprintf("%d%% cache hit", status.CacheHitPercent)
	}
	return statusText
}

func reviewerStatusEntryRole(status ReviewerStatus) string {
	switch strings.TrimSpace(status.Outcome) {
	case "failed", "followup_failed":
		return string(transcript.EntryRoleReviewerError)
	default:
		return string(transcript.EntryRoleReviewerStatus)
	}
}

func reviewerSuggestionsText(suggestions []string) string {
	if len(suggestions) == 0 {
		return ""
	}
	b := strings.Builder{}
	b.WriteString("Supervisor suggested:\n")
	for idx, suggestion := range suggestions {
		b.WriteString(strconv.Itoa(idx + 1))
		b.WriteString(". ")
		b.WriteString(strings.TrimSpace(suggestion))
		if idx < len(suggestions)-1 {
			b.WriteString("\n")
		}
	}
	return b.String()
}

func reviewerSessionID(sessionID string) string {
	trimmed := strings.TrimSpace(sessionID)
	if trimmed == "" {
		return ""
	}
	return trimmed + "/supervisor"
}

func appendMissingReviewerMetaContext(messages []llm.Message, workspaceRoot string, model string, thinkingLevel string, globalConfigDir string, headless bool, disabledSkills map[string]bool) ([]llm.Message, error) {
	metaMessages, transcript := splitMetaContextMessages(messages)
	metaMessages = filterReviewerMetaMessages(metaMessages)
	builder := newMetaContextBuilder(workspaceRoot, model, thinkingLevel, disabledSkills, time.Now()).withGlobalConfigDir(globalConfigDir)
	metaResult, err := builder.Build(metaContextBuildOptions{
		ExistingMessages:          metaMessages,
		IncludeAgents:             true,
		IncludeSkills:             true,
		IncludeEnvironment:        true,
		IncludeHeadless:           headless,
		PermissiveAgentsReadError: true,
	})
	if err != nil {
		return nil, err
	}
	out := append(metaResult.OrderedMetaMessages(), transcript...)
	return out, nil
}

func filterReviewerMetaMessages(messages []llm.Message) []llm.Message {
	if len(messages) == 0 {
		return nil
	}
	out := make([]llm.Message, 0, len(messages))
	for _, message := range messages {
		if message.Role == llm.RoleDeveloper && message.MessageType == llm.MessageTypeSubagents {
			continue
		}
		out = append(out, message)
	}
	return out
}

func pluralizeEnglish(count int, singular, plural string) string {
	if count == 1 {
		return fmt.Sprintf("%d %s", count, singular)
	}
	return fmt.Sprintf("%d %s", count, plural)
}
