package sessionruntime

import (
	"fmt"
	"strings"

	"core/server/tools"
)

type validatedQuestionBatchDescriptor struct {
	promptID         string
	candidateOrdinal int
	promptIDs        []string
}

func validateQuestionBatchMetadata(request tools.AskQuestionRequest) (*validatedQuestionBatchDescriptor, error) {
	batch := request.QuestionBatch
	if batch == nil {
		return nil, nil
	}
	invalid := func(detail string) (*validatedQuestionBatchDescriptor, error) {
		return nil, PromptBatchInvariantError{PromptID: request.ID, Detail: detail}
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
	if !normalizedQuestionBatchID(request.ID) {
		return invalid(fmt.Sprintf("request prompt id %q is blank or not normalized", request.ID))
	}
	if !normalizedQuestionBatchID(batch.PromptID) || batch.PromptID != request.ID {
		return invalid(fmt.Sprintf("metadata prompt id %q does not match request id", batch.PromptID))
	}
	if batch.PreparedPromptCount != len(batch.BatchPromptIDs) {
		return invalid(fmt.Sprintf(
			"prepared prompt count %d does not match prompt id count %d",
			batch.PreparedPromptCount,
			len(batch.BatchPromptIDs),
		))
	}
	if batch.CandidateOrdinal < 0 || batch.CandidateOrdinal >= len(batch.BatchPromptIDs) {
		return invalid(fmt.Sprintf(
			"candidate ordinal %d is outside %d prompt ids",
			batch.CandidateOrdinal,
			len(batch.BatchPromptIDs),
		))
	}
	promptIDs := make([]string, len(batch.BatchPromptIDs))
	seen := make(map[string]struct{}, len(batch.BatchPromptIDs))
	for index, promptID := range batch.BatchPromptIDs {
		if !normalizedQuestionBatchID(promptID) {
			return invalid(fmt.Sprintf("prompt id at index %d is blank or not normalized", index))
		}
		if _, exists := seen[promptID]; exists {
			return invalid(fmt.Sprintf("prompt id %q is duplicated", promptID))
		}
		seen[promptID] = struct{}{}
		promptIDs[index] = promptID
	}
	if promptIDs[batch.CandidateOrdinal] != request.ID {
		return invalid(fmt.Sprintf(
			"prompt id at candidate ordinal %d is %q",
			batch.CandidateOrdinal,
			promptIDs[batch.CandidateOrdinal],
		))
	}
	return &validatedQuestionBatchDescriptor{
		promptID:         request.ID,
		candidateOrdinal: batch.CandidateOrdinal,
		promptIDs:        promptIDs,
	}, nil
}

func (d *validatedQuestionBatchDescriptor) successorPromptIDs() []string {
	if d == nil || d.candidateOrdinal+1 >= len(d.promptIDs) {
		return nil
	}
	return append([]string(nil), d.promptIDs[d.candidateOrdinal+1:]...)
}

func normalizedQuestionBatchID(value string) bool {
	return value != "" && strings.TrimSpace(value) == value
}
