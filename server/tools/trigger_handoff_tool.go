package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"core/server/llm"
)

type TriggerHandoffController interface {
	TriggerHandoff(ctx context.Context, stepID string, activeCall llm.ToolCall, summarizerPrompt string, futureAgentMessage string) (string, bool, error)
}

type TriggerHandoffResultPayload struct {
	Summary                 string `json:"summary"`
	FutureAgentMessageAdded bool   `json:"future_agent_message_added,omitempty"`
}

type input struct {
	SummarizerPrompt   string `json:"summarizer_prompt,omitempty"`
	FutureAgentMessage string `json:"future_agent_message,omitempty"`
}

type TriggerHandoffTool struct {
	getController func() TriggerHandoffController
}

func NewTriggerHandoffTool(getController func() TriggerHandoffController) *TriggerHandoffTool {
	return &TriggerHandoffTool{getController: getController}
}

func (t *TriggerHandoffTool) Call(ctx context.Context, c Call) (Result, error) {
	if t == nil || t.getController == nil {
		return ErrorResult(c, "trigger_handoff controller is unavailable"), nil
	}
	controller := t.getController()
	if controller == nil {
		return ErrorResult(c, "trigger_handoff controller is unavailable"), nil
	}

	var in input
	if len(c.Input) > 0 {
		if err := json.Unmarshal(c.Input, &in); err != nil {
			return ErrorResult(c, fmt.Sprintf("invalid trigger_handoff input: %v. Provide an object with optional string fields `summarizer_prompt` and `future_agent_message`.", err)), nil
		}
	}

	summary, futureAgentMessageAdded, err := controller.TriggerHandoff(
		ctx,
		c.StepID,
		llm.ToolCall{ID: c.ID, Name: string(c.Name), Input: append(json.RawMessage(nil), c.Input...)},
		strings.TrimSpace(in.SummarizerPrompt),
		strings.TrimSpace(in.FutureAgentMessage),
	)
	if err != nil {
		message := strings.TrimSpace(err.Error())
		if message == "" {
			message = "trigger_handoff failed"
		} else {
			message = "trigger_handoff failed: " + message
		}
		return ErrorResult(c, message+". Retry only after the developer compaction reminder is present, or keep working if the user disabled handoff."), nil
	}
	payload := TriggerHandoffResultPayload{Summary: summary, FutureAgentMessageAdded: futureAgentMessageAdded}
	if strings.TrimSpace(payload.Summary) == "" {
		payload.Summary = "Handoff triggered."
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return Result{}, err
	}
	return Result{CallID: c.ID, Name: c.Name, Output: body}, nil
}
