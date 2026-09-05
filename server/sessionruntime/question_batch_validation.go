package sessionruntime

import (
	"fmt"
	"strings"

	"core/server/tools"
)

type validatedQuestionBatchDescriptor struct {
	toolCallID       string
	candidateOrdinal int
	toolCallIDs      []string
}

func validateQuestionBatchMetadata(request tools.AskQuestionRequest) (*validatedQuestionBatchDescriptor, error) {
	batch := request.QuestionBatch
	if batch == nil {
		return nil, nil
	}
	invalid := func(detail string) (*validatedQuestionBatchDescriptor, error) {
		return nil, PromptBatchInvariantError{ToolCallID: request.ToolCallID, Detail: detail}
	}
	if batch.Origin != tools.AskQuestionOriginModelTool {
		return invalid(fmt.Sprintf("origin is %q", batch.Origin))
	}
	if request.Origin != tools.AskQuestionOriginModelTool || batch.Origin != request.Origin {
		return invalid(fmt.Sprintf("origin %q does not match request origin %q", batch.Origin, request.Origin))
	}
	if !normalizedQuestionBatchID(request.RunID) {
		return invalid(fmt.Sprintf("request run id %q is blank or not normalized", request.RunID))
	}
	if !normalizedQuestionBatchID(batch.RunID) || batch.RunID != request.RunID {
		return invalid(fmt.Sprintf("run id %q does not match request run id %q", batch.RunID, request.RunID))
	}
	if !normalizedQuestionBatchID(request.StepID) {
		return invalid(fmt.Sprintf("request step id %q is blank or not normalized", request.StepID))
	}
	if !normalizedQuestionBatchID(batch.StepID) || batch.StepID != request.StepID {
		return invalid(fmt.Sprintf("step id %q does not match request step id %q", batch.StepID, request.StepID))
	}
	if !normalizedQuestionBatchID(request.ToolCallID) {
		return invalid(fmt.Sprintf("request tool call id %q is blank or not normalized", request.ToolCallID))
	}
	if !normalizedQuestionBatchID(batch.ToolCallID) || batch.ToolCallID != request.ToolCallID {
		return invalid(fmt.Sprintf("metadata tool call id %q does not match request tool call id", batch.ToolCallID))
	}
	if batch.PreparedPromptCount != len(batch.BatchToolCallIDs) {
		return invalid(fmt.Sprintf(
			"prepared prompt count %d does not match tool call id count %d",
			batch.PreparedPromptCount,
			len(batch.BatchToolCallIDs),
		))
	}
	if batch.CandidateOrdinal < 0 || batch.CandidateOrdinal >= len(batch.BatchToolCallIDs) {
		return invalid(fmt.Sprintf(
			"candidate ordinal %d is outside %d tool call ids",
			batch.CandidateOrdinal,
			len(batch.BatchToolCallIDs),
		))
	}
	toolCallIDs := make([]string, len(batch.BatchToolCallIDs))
	seen := make(map[string]struct{}, len(batch.BatchToolCallIDs))
	for index, toolCallID := range batch.BatchToolCallIDs {
		if !normalizedQuestionBatchID(toolCallID) {
			return invalid(fmt.Sprintf("tool call id at index %d is blank or not normalized", index))
		}
		if _, exists := seen[toolCallID]; exists {
			return invalid(fmt.Sprintf("tool call id %q is duplicated", toolCallID))
		}
		seen[toolCallID] = struct{}{}
		toolCallIDs[index] = toolCallID
	}
	if toolCallIDs[batch.CandidateOrdinal] != request.ToolCallID {
		return invalid(fmt.Sprintf(
			"tool call id at candidate ordinal %d is %q",
			batch.CandidateOrdinal,
			toolCallIDs[batch.CandidateOrdinal],
		))
	}
	return &validatedQuestionBatchDescriptor{
		toolCallID:       request.ToolCallID,
		candidateOrdinal: batch.CandidateOrdinal,
		toolCallIDs:      toolCallIDs,
	}, nil
}

func (d *validatedQuestionBatchDescriptor) successorToolCallIDs() []string {
	if d == nil || d.candidateOrdinal+1 >= len(d.toolCallIDs) {
		return nil
	}
	return append([]string(nil), d.toolCallIDs[d.candidateOrdinal+1:]...)
}

func normalizedQuestionBatchID(value string) bool {
	return value != "" && strings.TrimSpace(value) == value
}
