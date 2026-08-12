package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"

	"core/prompts"
	"core/shared/clientui"
	"core/shared/sessioncontract"
	"core/shared/textutil"
	"core/shared/toolspec"

	"github.com/google/uuid"
)

// AskQuestionRequest is the internal broker request. It is intentionally not the
// model-facing tool payload shape because internal approval workflows carry
// fields that must never be exposed through the ask_question tool contract.
type AskQuestionRequest struct {
	ID                     string                                `json:"-"`
	Question               string                                `json:"-"`
	Suggestions            []string                              `json:"-"`
	RecommendedOptionIndex int                                   `json:"-"`
	Approval               bool                                  `json:"-"`
	ApprovalOptions        []AskQuestionApprovalOption           `json:"-"`
	Origin                 AskQuestionOrigin                     `json:"-"`
	RunID                  string                                `json:"-"`
	StepID                 string                                `json:"-"`
	ToolCallID             string                                `json:"-"`
	QuestionBatch          *AskQuestionBatchMetadata             `json:"-"`
	AttentionTarget        *clientui.AttentionNotificationTarget `json:"-"`
}

func (r AskQuestionRequest) IsTaskScopedApprovalQuestion() bool {
	if !r.Approval || r.AttentionTarget == nil || r.AttentionTarget.Focus == nil {
		return false
	}
	if r.AttentionTarget.Kind != clientui.AttentionNotificationTargetWorkflowTask {
		return false
	}
	return r.AttentionTarget.Focus.Kind == clientui.AttentionNotificationFocusQuestion
}

// AskQuestionToolRequest is the model-facing ask_question payload. Keep this limited to
// ordinary question flows; internal approval uses AskQuestionRequest instead.
type AskQuestionToolRequest struct {
	Question               string   `json:"question" jsonschema_description:"Question text shown to the user. You must only put exactly ONE question and the context needed to answer it here. The text is markdown-formatted."`
	Suggestions            []string `json:"suggestions,omitempty" jsonschema_description:"Optional choice suggestions. Omit this field when you want a freeform-only answer. If you provide >1 suggestions, provide recommended_option_index. Strive to give users the best, sensible options possible, following best-practices, guidelines, and common sense. Omit 'Other' or similar generic options - the system already appends that option."`
	RecommendedOptionIndex int      `json:"recommended_option_index,omitempty" jsonschema_description:"Optional 1-based index of the recommended suggestion, omit to not state a preference."`
}

func AskQuestionStaticContractSource() StaticContractSource {
	return StaticContractSource{ID: toolspec.ToolAskQuestion, Input: AskQuestionToolRequest{}}
}

// Validation sentinels for request/response shape errors. Tests match these
// via errors.Is rather than asserting message wording.
var (
	ErrAskQuestionApprovalRequiresOptions       = errors.New("approval questions require approval_options")
	ErrAskQuestionApprovalForbidsSuggestions    = errors.New("approval questions must not set suggestions")
	ErrAskQuestionApprovalForbidsRecommended    = errors.New("approval questions must not set recommended_option_index")
	ErrAskQuestionApprovalRequiresResponse      = errors.New("approval questions require approval responses")
	ErrAskQuestionApprovalForbidsOrdinaryAnswer = errors.New("approval questions must not return ordinary answer fields")
	ErrAskQuestionNonApprovalForbidsApproval    = errors.New("non-approval questions must not return approval payloads")
	ErrAskQuestionNonApprovalRequiresAnswer     = sessioncontract.ErrPromptQuestionAnswerRequired
	ErrAskQuestionSelectedOptionRequiresSuggest = errors.New("selected option numbers require suggestions")
)

type AskQuestionApprovalDecision = sessioncontract.PromptApprovalDecision

const (
	AskQuestionApprovalDecisionAllowOnce    = sessioncontract.PromptApprovalDecisionAllowOnce
	AskQuestionApprovalDecisionAllowSession = sessioncontract.PromptApprovalDecisionAllowSession
	AskQuestionApprovalDecisionDeny         = sessioncontract.PromptApprovalDecisionDeny
)

type AskQuestionApprovalOption struct {
	Decision AskQuestionApprovalDecision `json:"decision"`
	Label    string                      `json:"label"`
}

type AskQuestionBroker struct {
	mu    sync.Mutex
	queue []*pending
	// onAsk switches the broker into synchronous handler mode. When unset, Ask
	// uses queued submit mode and requests complete only via Submit.
	onAsk func(context.Context, AskQuestionRequest) (AskQuestionResolution, error)
}

type pending struct {
	req       AskQuestionRequest
	ch        chan responseResult
	completed bool
}

type responseResult struct {
	resolution AskQuestionResolution
	err        error
}

func NewAskQuestionBroker() *AskQuestionBroker {
	return &AskQuestionBroker{}
}

func (b *AskQuestionBroker) SetAskHandler(handler func(context.Context, AskQuestionRequest) (AskQuestionResolution, error)) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.onAsk = handler
}

func (b *AskQuestionBroker) Ask(ctx context.Context, req AskQuestionRequest) (AskQuestionResolution, error) {
	if req.ID == "" {
		req.ID = uuid.NewString()
	}
	req.Suggestions = normalizedSuggestions(req.Suggestions)
	req.RecommendedOptionIndex = normalizedRecommendedOptionIndex(req.RecommendedOptionIndex, len(req.Suggestions))
	if req.Question == "" {
		return nil, errors.New("question is required")
	}
	if err := validateRequest(req); err != nil {
		return nil, err
	}
	if barrier, ok := EffectBarrierFromContext(ctx); ok {
		reason, err := effectBarrierReasonForAsk(req)
		if err != nil {
			return nil, err
		}
		if err := barrier(reason); err != nil {
			return nil, err
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
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

func (b *AskQuestionBroker) askHandler() func(context.Context, AskQuestionRequest) (AskQuestionResolution, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.onAsk
}

func (b *AskQuestionBroker) askSync(ctx context.Context, req AskQuestionRequest, handler func(context.Context, AskQuestionRequest) (AskQuestionResolution, error)) (AskQuestionResolution, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	resolution, err := handler(ctx, req)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := ValidateAskQuestionResolution(req, resolution); err != nil {
		return nil, err
	}
	return resolution, nil
}

func (b *AskQuestionBroker) askQueued(ctx context.Context, req AskQuestionRequest) (AskQuestionResolution, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	p := &pending{req: req, ch: make(chan responseResult, 1)}
	b.mu.Lock()
	b.queue = append(b.queue, p)
	b.mu.Unlock()
	defer b.dequeue(req.ID)

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case rr := <-p.ch:
		return b.finishQueuedResponse(req, rr)
	}
}

func (b *AskQuestionBroker) finishQueuedResponse(req AskQuestionRequest, rr responseResult) (AskQuestionResolution, error) {
	if rr.err != nil {
		return nil, rr.err
	}
	if err := ValidateAskQuestionResolution(req, rr.resolution); err != nil {
		return nil, err
	}
	return rr.resolution, nil
}

func (b *AskQuestionBroker) Submit(requestID string, resolution AskQuestionResolution) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, p := range b.queue {
		if p.req.ID == requestID {
			return b.deliverPendingResponseLocked(p, responseResult{resolution: resolution})
		}
	}
	return fmt.Errorf("request %s not found", requestID)
}

func (b *AskQuestionBroker) deliverPendingResponseLocked(p *pending, rr responseResult) error {
	if p.completed {
		return fmt.Errorf("request %s already completed", p.req.ID)
	}
	if rr.err == nil {
		if err := ValidateAskQuestionResolution(p.req, rr.resolution); err != nil {
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
		if err := sessioncontract.ValidatePromptApprovalDecision(option.Decision); err != nil {
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

func selectedOptionToolOutputSummary(optionNumber int, freeform *string) string {
	base := fmt.Sprintf("User chose option #%d.", optionNumber)
	if freeform == nil {
		return base
	}
	return base + " They also said: " + *freeform
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

func DecodeAskQuestionToolRequest(callID string, input json.RawMessage) (AskQuestionRequest, error) {
	var in AskQuestionToolRequest
	if err := json.Unmarshal(input, &in); err != nil {
		return AskQuestionRequest{}, fmt.Errorf("invalid input: %w", err)
	}
	req := in.request(callID)
	req.Suggestions = normalizedSuggestions(req.Suggestions)
	req.RecommendedOptionIndex = normalizedRecommendedOptionIndex(req.RecommendedOptionIndex, len(req.Suggestions))
	if req.Question == "" {
		return AskQuestionRequest{}, errors.New("question is required")
	}
	if err := validateRequest(req); err != nil {
		return AskQuestionRequest{}, err
	}
	return req, nil
}

type AskQuestionTool struct {
	broker           *AskQuestionBroker
	questionsEnabled func() bool
}

func NewAskQuestionTool(b *AskQuestionBroker, questionsEnabled func() bool) *AskQuestionTool {
	return &AskQuestionTool{broker: b, questionsEnabled: questionsEnabled}
}

func (t *AskQuestionTool) QuestionsEnabled() bool {
	return t == nil || t.questionsEnabled == nil || t.questionsEnabled()
}

func (t *AskQuestionTool) Call(ctx context.Context, c Call) (Result, error) {
	if !t.QuestionsEnabled() {
		notifyAskQuestionBatchSkipped(c)
		return ErrorResult(c, prompts.QuestionsDisabledPrompt), nil
	}
	req, prepareErr := DecodeAskQuestionToolRequest(c.ID, c.Input)
	if prepareErr != nil {
		notifyAskQuestionBatchSkipped(c)
		return ErrorResult(c, prepareErr.Error()), nil
	}
	req.Origin = AskQuestionOriginModelTool
	req.RunID = c.RunID
	req.StepID = c.StepID
	req.ToolCallID = c.ID
	if c.AskQuestionBatch != nil {
		batch := *c.AskQuestionBatch
		batch.BatchPromptIDs = append([]string(nil), c.AskQuestionBatch.BatchPromptIDs...)
		req.Origin = batch.Origin
		req.RunID = batch.RunID
		req.StepID = batch.StepID
		req.ToolCallID = c.ID
		req.QuestionBatch = &batch
	}
	resolution, err := t.broker.Ask(ctx, req)
	if err != nil {
		if ShouldSkipRemainingQuestionBatch(err, context.Cause(ctx)) {
			notifyAskQuestionBatchSkipped(c)
		}
		return ErrorResult(c, err.Error()), nil
	}
	summary, summaryErr := buildResolutionToolOutputSummary(resolution)
	if summaryErr != nil {
		return Result{}, summaryErr
	}
	body, marshalErr := json.Marshal(summary)
	if marshalErr != nil {
		return Result{}, marshalErr
	}
	condensed, condensedErr := buildResolutionCondensedToolOutputText(req, resolution)
	if condensedErr != nil {
		return Result{}, condensedErr
	}
	return Result{
		CallID: c.ID, Name: c.Name, Output: body,
		CondensedText: textutil.OptionalExactString(condensed),
	}, nil
}

// ShouldSkipRemainingQuestionBatch reports whether prepared ask_question
// successors can no longer materialize. A declined prompt returns
// context.Canceled while its execution context remains live, so it does not
// skip those successors.
func ShouldSkipRemainingQuestionBatch(askErr error, executionErr error) bool {
	return executionErr != nil || errors.Is(askErr, io.EOF)
}

func notifyAskQuestionBatchSkipped(c Call) {
	if c.AskQuestionBatch == nil || c.OnAskQuestionBatchSkipped == nil {
		return
	}
	batch := *c.AskQuestionBatch
	batch.BatchPromptIDs = append([]string(nil), c.AskQuestionBatch.BatchPromptIDs...)
	c.OnAskQuestionBatchSkipped(batch)
}
