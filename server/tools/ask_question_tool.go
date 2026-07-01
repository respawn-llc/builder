package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	"core/prompts"

	"github.com/google/uuid"
)

// AskQuestionRequest is the internal broker request. It is intentionally not the
// model-facing tool payload shape because internal approval workflows carry
// fields that must never be exposed through the ask_question tool contract.
type AskQuestionRequest struct {
	ID                     string                      `json:"-"`
	Question               string                      `json:"-"`
	Suggestions            []string                    `json:"-"`
	RecommendedOptionIndex int                         `json:"-"`
	Approval               bool                        `json:"-"`
	ApprovalOptions        []AskQuestionApprovalOption `json:"-"`
}

// AskQuestionToolRequest is the model-facing ask_question payload. Keep this limited to
// ordinary question flows; internal approval uses AskQuestionRequest instead.
type AskQuestionToolRequest struct {
	Question               string   `json:"question"`
	Suggestions            []string `json:"suggestions,omitempty"`
	RecommendedOptionIndex int      `json:"recommended_option_index,omitempty"`
}

// Validation sentinels for request/response shape errors. Tests match these
// via errors.Is rather than asserting message wording.
var (
	ErrAskQuestionApprovalRequiresOptions       = errors.New("approval questions require approval_options")
	ErrAskQuestionApprovalForbidsSuggestions    = errors.New("approval questions must not set suggestions")
	ErrAskQuestionApprovalForbidsRecommended    = errors.New("approval questions must not set recommended_option_index")
	ErrAskQuestionApprovalRequiresResponse      = errors.New("approval questions require approval responses")
	ErrAskQuestionNonApprovalForbidsApproval    = errors.New("non-approval questions must not return approval payloads")
	ErrAskQuestionNonApprovalRequiresAnswer     = errors.New("non-approval questions require an answer")
	ErrAskQuestionSelectedOptionRequiresSuggest = errors.New("selected option numbers require suggestions")
)

type AskQuestionApprovalDecision string

const (
	AskQuestionApprovalDecisionAllowOnce    AskQuestionApprovalDecision = "allow_once"
	AskQuestionApprovalDecisionAllowSession AskQuestionApprovalDecision = "allow_session"
	AskQuestionApprovalDecisionDeny         AskQuestionApprovalDecision = "deny"
)

type AskQuestionApprovalOption struct {
	Decision AskQuestionApprovalDecision `json:"decision"`
	Label    string                      `json:"label"`
}

type AskQuestionApprovalPayload struct {
	Decision   AskQuestionApprovalDecision `json:"decision"`
	Commentary string                      `json:"commentary,omitempty"`
}

type AskQuestionResponse struct {
	RequestID            string                      `json:"request_id"`
	Answer               string                      `json:"answer,omitempty"`
	SelectedOptionNumber int                         `json:"selected_option_number,omitempty"`
	FreeformAnswer       string                      `json:"freeform_answer,omitempty"`
	Approval             *AskQuestionApprovalPayload `json:"approval,omitempty"`
}

type AskQuestionBroker struct {
	mu    sync.Mutex
	queue []*pending
	// onAsk switches the broker into synchronous handler mode. When unset, Ask
	// uses queued submit mode and requests complete only via Submit.
	onAsk func(AskQuestionRequest) (AskQuestionResponse, error)
}

type pending struct {
	req       AskQuestionRequest
	ch        chan responseResult
	completed bool
}

type responseResult struct {
	response AskQuestionResponse
	err      error
}

func NewAskQuestionBroker() *AskQuestionBroker {
	return &AskQuestionBroker{}
}

func (b *AskQuestionBroker) SetAskHandler(handler func(AskQuestionRequest) (AskQuestionResponse, error)) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.onAsk = handler
}

func (b *AskQuestionBroker) Ask(ctx context.Context, req AskQuestionRequest) (AskQuestionResponse, error) {
	if req.ID == "" {
		req.ID = uuid.NewString()
	}
	req.Suggestions = normalizedSuggestions(req.Suggestions)
	req.RecommendedOptionIndex = normalizedRecommendedOptionIndex(req.RecommendedOptionIndex, len(req.Suggestions))
	if req.Question == "" {
		return AskQuestionResponse{}, errors.New("question is required")
	}
	if err := validateRequest(req); err != nil {
		return AskQuestionResponse{}, err
	}

	h := b.askHandler()
	if h != nil {
		// Synchronous handler mode has exactly one completion path: the handler
		// return value. Requests are never queued in this mode.
		return b.askSync(ctx, req, h)
	}
	// Queued submit mode has exactly one completion path: Submit delivering a
	// validated response to the pending request.
	return b.askQueued(ctx, req)
}

func (b *AskQuestionBroker) askHandler() func(AskQuestionRequest) (AskQuestionResponse, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.onAsk
}

func (b *AskQuestionBroker) askSync(ctx context.Context, req AskQuestionRequest, handler func(AskQuestionRequest) (AskQuestionResponse, error)) (AskQuestionResponse, error) {
	if err := ctx.Err(); err != nil {
		return AskQuestionResponse{}, err
	}
	resp, err := handler(req)
	if err != nil {
		return AskQuestionResponse{}, err
	}
	if err := ctx.Err(); err != nil {
		return AskQuestionResponse{}, err
	}
	if resp.RequestID == "" {
		resp.RequestID = req.ID
	}
	if err := validateResponse(req, resp); err != nil {
		return AskQuestionResponse{}, err
	}
	return resp, nil
}

func (b *AskQuestionBroker) askQueued(ctx context.Context, req AskQuestionRequest) (AskQuestionResponse, error) {

	p := &pending{req: req, ch: make(chan responseResult, 1)}
	b.mu.Lock()
	b.queue = append(b.queue, p)
	b.mu.Unlock()
	defer b.dequeue(req.ID)

	select {
	case <-ctx.Done():
		return AskQuestionResponse{}, ctx.Err()
	case rr := <-p.ch:
		return b.finishQueuedResponse(req, rr)
	}
}

func (b *AskQuestionBroker) finishQueuedResponse(req AskQuestionRequest, rr responseResult) (AskQuestionResponse, error) {
	if rr.err != nil {
		return AskQuestionResponse{}, rr.err
	}
	if rr.response.RequestID == "" {
		rr.response.RequestID = req.ID
	}
	if err := validateResponse(req, rr.response); err != nil {
		return AskQuestionResponse{}, err
	}
	return rr.response, nil
}

func (b *AskQuestionBroker) Submit(requestID string, resp AskQuestionResponse) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, p := range b.queue {
		if p.req.ID == requestID {
			return b.deliverPendingResponseLocked(p, responseResult{response: resp})
		}
	}
	return fmt.Errorf("request %s not found", requestID)
}

func (b *AskQuestionBroker) deliverPendingResponseLocked(p *pending, rr responseResult) error {
	if p.completed {
		return fmt.Errorf("request %s already completed", p.req.ID)
	}
	if rr.err == nil {
		if rr.response.RequestID == "" {
			rr.response.RequestID = p.req.ID
		}
		if err := validateResponse(p.req, rr.response); err != nil {
			return err
		}
	}
	p.completed = true
	p.ch <- rr
	return nil
}

func validateRequest(req AskQuestionRequest) error {
	if req.Approval {
		if req.RecommendedOptionIndex != 0 {
			return ErrAskQuestionApprovalForbidsRecommended
		}
		if len(req.Suggestions) > 0 {
			return ErrAskQuestionApprovalForbidsSuggestions
		}
	}
	if !req.Approval {
		return nil
	}
	if len(req.ApprovalOptions) == 0 {
		return ErrAskQuestionApprovalRequiresOptions
	}
	seen := make(map[AskQuestionApprovalDecision]struct{}, len(req.ApprovalOptions))
	for _, option := range req.ApprovalOptions {
		if err := validateApprovalDecision(option.Decision); err != nil {
			return fmt.Errorf("invalid approval option: %w", err)
		}
		if option.Label == "" {
			return fmt.Errorf("approval option %q requires a label", option.Decision)
		}
		if _, ok := seen[option.Decision]; ok {
			return fmt.Errorf("duplicate approval option %q", option.Decision)
		}
		seen[option.Decision] = struct{}{}
	}
	return nil
}

func normalizedSuggestions(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	out := make([]string, 0, len(in))
	for _, suggestion := range in {
		trimmed := strings.TrimSpace(suggestion)
		if trimmed == "" {
			continue
		}
		out = append(out, trimmed)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func normalizedRecommendedOptionIndex(index int, suggestionCount int) int {
	if suggestionCount == 0 {
		return 0
	}
	if index < 1 || index > suggestionCount {
		return 0
	}
	return index
}

func validateResponse(req AskQuestionRequest, resp AskQuestionResponse) error {
	if !req.Approval {
		if resp.Approval != nil {
			return ErrAskQuestionNonApprovalForbidsApproval
		}
		if resp.SelectedOptionNumber > 0 {
			if len(req.Suggestions) == 0 {
				return ErrAskQuestionSelectedOptionRequiresSuggest
			}
			if resp.SelectedOptionNumber > len(req.Suggestions) {
				return fmt.Errorf("selected option number %d is out of range", resp.SelectedOptionNumber)
			}
			return nil
		}
		if normalizedFreeformAnswer(resp) == "" {
			return ErrAskQuestionNonApprovalRequiresAnswer
		}
		return nil
	}
	if resp.Approval == nil {
		return ErrAskQuestionApprovalRequiresResponse
	}
	return validateApprovalDecision(resp.Approval.Decision)
}

func normalizedFreeformAnswer(resp AskQuestionResponse) string {
	if trimmed := strings.TrimSpace(resp.FreeformAnswer); trimmed != "" {
		return trimmed
	}
	return strings.TrimSpace(resp.Answer)
}

func buildToolOutputSummary(resp AskQuestionResponse) (string, error) {
	freeform := normalizedFreeformAnswer(resp)
	if resp.SelectedOptionNumber > 0 {
		return selectedOptionToolOutputSummary(resp.SelectedOptionNumber, freeform), nil
	}
	if freeform == "" {
		return "", ErrAskQuestionNonApprovalRequiresAnswer
	}
	return "User answered: " + freeform, nil
}

func selectedOptionToolOutputSummary(optionNumber int, freeform string) string {
	base := fmt.Sprintf("User chose option #%d.", optionNumber)
	if freeform == "" {
		return base
	}
	return base + " They also said: " + freeform
}

func buildCondensedToolOutputText(req AskQuestionRequest, resp AskQuestionResponse) string {
	freeform := normalizedFreeformAnswer(resp)
	if resp.SelectedOptionNumber <= 0 {
		return freeform
	}
	suggestions := normalizedSuggestions(req.Suggestions)
	optionIndex := resp.SelectedOptionNumber - 1
	if optionIndex < 0 || optionIndex >= len(suggestions) {
		return ""
	}
	base := suggestions[optionIndex]
	if freeform == "" {
		return base
	}
	return base + "\nUser also said:\n" + freeform
}

func validateApprovalDecision(decision AskQuestionApprovalDecision) error {
	switch decision {
	case AskQuestionApprovalDecisionAllowOnce, AskQuestionApprovalDecisionAllowSession, AskQuestionApprovalDecisionDeny:
		return nil
	default:
		return fmt.Errorf("unsupported approval decision %q", decision)
	}
}

func (b *AskQuestionBroker) Pending() []AskQuestionRequest {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]AskQuestionRequest, 0, len(b.queue))
	for _, p := range b.queue {
		out = append(out, p.req)
	}
	return out
}

func (b *AskQuestionBroker) dequeue(requestID string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]*pending, 0, len(b.queue))
	for _, p := range b.queue {
		if p.req.ID == requestID {
			continue
		}
		out = append(out, p)
	}
	b.queue = out
}

func (r AskQuestionToolRequest) request(callID string) AskQuestionRequest {
	return AskQuestionRequest{
		ID:                     callID,
		Question:               r.Question,
		Suggestions:            r.Suggestions,
		RecommendedOptionIndex: r.RecommendedOptionIndex,
	}
}

type AskQuestionTool struct {
	broker           *AskQuestionBroker
	questionsEnabled func() bool
}

func NewAskQuestionTool(b *AskQuestionBroker, questionsEnabled func() bool) *AskQuestionTool {
	return &AskQuestionTool{broker: b, questionsEnabled: questionsEnabled}
}

func (t *AskQuestionTool) Call(ctx context.Context, c Call) (Result, error) {
	if t.questionsEnabled != nil && !t.questionsEnabled() {
		return ErrorResult(c, prompts.QuestionsDisabledPrompt), nil
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(c.Input, &raw); err != nil {
		return ErrorResult(c, fmt.Sprintf("invalid input: %v", err)), nil
	}
	if _, ok := raw["action"]; ok {
		return ErrorResult(c, "invalid input: field \"action\" is not allowed"), nil
	}
	if _, ok := raw["approval"]; ok {
		return ErrorResult(c, "invalid input: field \"approval\" is not allowed"), nil
	}
	if _, ok := raw["approval_options"]; ok {
		return ErrorResult(c, "invalid input: field \"approval_options\" is not allowed"), nil
	}

	var in AskQuestionToolRequest
	if err := json.Unmarshal(c.Input, &in); err != nil {
		return ErrorResult(c, fmt.Sprintf("invalid input: %v", err)), nil
	}
	req := in.request(c.ID)
	resp, err := t.broker.Ask(ctx, req)
	if err != nil {
		return ErrorResult(c, err.Error()), nil
	}
	summary, summaryErr := buildToolOutputSummary(resp)
	if summaryErr != nil {
		return Result{}, summaryErr
	}
	body, marshalErr := json.Marshal(summary)
	if marshalErr != nil {
		return Result{}, marshalErr
	}
	return Result{CallID: c.ID, Name: c.Name, Output: body, CondensedText: buildCondensedToolOutputText(req, resp)}, nil
}
