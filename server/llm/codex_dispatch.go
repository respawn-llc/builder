package llm

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"

	"github.com/openai/openai-go/v3/responses"
	"golang.org/x/net/http/httpguts"
)

const codexTurnStateHeader = "x-codex-turn-state"

type CodexRequestKind string

const (
	CodexRequestKindTurn       CodexRequestKind = "turn"
	CodexRequestKindCompaction CodexRequestKind = "compaction"
)

func (k CodexRequestKind) Optional() *CodexRequestKind {
	return &k
}

type CodexDispatchFacts struct {
	SessionID            string
	RunID                string
	CompactionGeneration int
	RequestKind          *CodexRequestKind
}

type CodexDispatchContext struct {
	facts CodexDispatchFacts
	state *CodexDispatchState
}

type CodexDispatchState struct {
	mu           sync.Mutex
	turnState    string
	hasTurnState bool
	diagnostics  map[CodexTurnStateDiagnosticCategory]struct{}
}

type CodexTurnStateDiagnosticCategory string

const (
	CodexTurnStateDiagnosticInvalid CodexTurnStateDiagnosticCategory = "provider_turn_state_invalid"
)

type codexTurnStateSource string

const (
	codexTurnStateSourceHTTPHeader codexTurnStateSource = "http_header"
)

type codexTurnMetadata struct {
	SessionID   string            `json:"session_id"`
	ThreadID    string            `json:"thread_id"`
	TurnID      string            `json:"turn_id"`
	WindowID    string            `json:"window_id"`
	RequestKind *CodexRequestKind `json:"request_kind,omitempty"`
}

type codexDispatchProjection struct {
	TurnMetadataJSON string
	RoutingHint      string
}

func NewCodexDispatchContext(facts CodexDispatchFacts) (*CodexDispatchContext, error) {
	if err := facts.validate(); err != nil {
		return nil, err
	}
	return &CodexDispatchContext{
		facts: facts,
		state: &CodexDispatchState{},
	}, nil
}

func (c *CodexDispatchContext) TurnMetadataJSON() (string, error) {
	if err := c.validate(); err != nil {
		return "", err
	}
	payload, err := json.Marshal(codexTurnMetadata{
		SessionID:   c.facts.SessionID,
		ThreadID:    c.facts.SessionID,
		TurnID:      c.facts.RunID,
		WindowID:    fmt.Sprintf("%s:%d", c.facts.SessionID, c.facts.CompactionGeneration),
		RequestKind: c.facts.RequestKind,
	})
	if err != nil {
		return "", fmt.Errorf("marshal Codex turn metadata: %w", err)
	}
	return string(payload), nil
}

func (c *CodexDispatchContext) outboundProjection(
	model string,
	serviceTier *responses.ResponseNewParamsServiceTier,
) (codexDispatchProjection, error) {
	metadata, err := c.TurnMetadataJSON()
	if err != nil {
		return codexDispatchProjection{}, err
	}
	routingHint, err := buildCodexRoutingHint(model, serviceTier)
	if err != nil {
		return codexDispatchProjection{}, err
	}
	return codexDispatchProjection{
		TurnMetadataJSON: metadata,
		RoutingHint:      routingHint,
	}, nil
}

func (c *CodexDispatchContext) turnStateForRetry() (string, bool) {
	return c.currentTurnState()
}

func (c *CodexDispatchContext) currentTurnState() (string, bool) {
	if c == nil || c.state == nil {
		return "", false
	}
	c.state.mu.Lock()
	defer c.state.mu.Unlock()
	return c.state.turnState, c.state.hasTurnState
}

func (c *CodexDispatchContext) observeTurnStateCandidate(value string, source codexTurnStateSource) {
	if c == nil || c.state == nil {
		return
	}
	c.state.mu.Lock()
	switch {
	case validateCodexHeaderValue(value) != nil:
		shouldWarn := c.state.recordDiagnosticLocked(CodexTurnStateDiagnosticInvalid)
		requestKind := c.facts.RequestKind
		c.state.mu.Unlock()
		c.warnUnusableTurnState(shouldWarn, CodexTurnStateDiagnosticInvalid, source, requestKind)
		return
	case !c.state.hasTurnState:
		c.state.turnState = value
		c.state.hasTurnState = true
		c.state.mu.Unlock()
		return
	default:
		c.state.mu.Unlock()
		return
	}
}

func (s *CodexDispatchState) recordDiagnosticLocked(category CodexTurnStateDiagnosticCategory) bool {
	if s.diagnostics == nil {
		s.diagnostics = make(map[CodexTurnStateDiagnosticCategory]struct{}, 1)
	}
	if _, exists := s.diagnostics[category]; exists {
		return false
	}
	s.diagnostics[category] = struct{}{}
	return true
}

func (c *CodexDispatchContext) warnUnusableTurnState(
	shouldWarn bool,
	category CodexTurnStateDiagnosticCategory,
	source codexTurnStateSource,
	requestKind *CodexRequestKind,
) {
	if !shouldWarn {
		return
	}
	attributes := []any{
		"category", category,
		"source", source,
	}
	if requestKind != nil {
		attributes = append(attributes, "request_kind", *requestKind)
	}
	slog.Warn("ignored unusable provider turn state", attributes...)
}

func (c *CodexDispatchContext) TurnStateDiagnostics() []CodexTurnStateDiagnosticCategory {
	if c == nil || c.state == nil {
		return nil
	}
	c.state.mu.Lock()
	defer c.state.mu.Unlock()
	if _, exists := c.state.diagnostics[CodexTurnStateDiagnosticInvalid]; exists {
		return []CodexTurnStateDiagnosticCategory{CodexTurnStateDiagnosticInvalid}
	}
	return nil
}

func (c *CodexDispatchContext) validate() error {
	if c == nil {
		return fmt.Errorf("%w: Codex dispatch context is required", ErrInvalidRequest)
	}
	if c.state == nil {
		return fmt.Errorf("%w: Codex dispatch state is required", ErrInvalidRequest)
	}
	return c.facts.validate()
}

func (c *CodexDispatchContext) validateForSession(sessionID string) error {
	if err := c.validate(); err != nil {
		return err
	}
	if sessionID != c.facts.SessionID {
		return fmt.Errorf(
			"%w: Codex dispatch session ID %q does not match request session ID %q",
			ErrInvalidRequest,
			c.facts.SessionID,
			sessionID,
		)
	}
	return nil
}

func validateOpenAIDispatchSessionID(sessionID string) error {
	if err := validateCodexHeaderValue(sessionID); err != nil {
		return fmt.Errorf("%w: Session ID is not wire-realizable: %v", ErrInvalidRequest, err)
	}
	return nil
}

func validateSessionDispatchPairing(sessionID *string, dispatch *CodexDispatchContext) error {
	if sessionID != nil {
		if err := validateOpenAIDispatchSessionID(*sessionID); err != nil {
			return err
		}
	}
	if dispatch == nil {
		return nil
	}
	if sessionID == nil {
		return fmt.Errorf("%w: Session identity is required with Codex dispatch context", ErrInvalidRequest)
	}
	return dispatch.validateForSession(*sessionID)
}

func validateOpenAIDispatch(
	sessionID string,
	model string,
	dispatch *CodexDispatchContext,
	isChatGPTCodex bool,
	serviceTier *responses.ResponseNewParamsServiceTier,
) (*codexDispatchProjection, error) {
	if !isChatGPTCodex {
		return nil, nil
	}
	if dispatch == nil {
		return nil, fmt.Errorf("%w: Codex dispatch context is required for OAuth dispatch", ErrInvalidRequest)
	}
	if err := dispatch.validateForSession(sessionID); err != nil {
		return nil, err
	}
	projection, err := dispatch.outboundProjection(model, serviceTier)
	if err != nil {
		return nil, err
	}
	return &projection, nil
}

func validateCodexHeaderValue(value string) error {
	if len(value) == 0 {
		return fmt.Errorf("value is required")
	}
	if value[0] == ' ' || value[0] == '\t' || value[len(value)-1] == ' ' || value[len(value)-1] == '\t' {
		return fmt.Errorf("value has leading or trailing SP/HTAB")
	}
	if !httpguts.ValidHeaderFieldValue(value) {
		return fmt.Errorf("value is not a valid HTTP header field value")
	}
	return nil
}

func buildCodexRoutingHint(model string, serviceTier *responses.ResponseNewParamsServiceTier) (string, error) {
	if len(model) == 0 {
		return "", fmt.Errorf("%w: Codex routing model is required", ErrInvalidRequest)
	}
	if model[0] == ' ' || model[0] == '\t' || model[len(model)-1] == ' ' || model[len(model)-1] == '\t' {
		return "", fmt.Errorf("%w: Codex routing model has leading or trailing SP/HTAB", ErrInvalidRequest)
	}
	if strings.Contains(model, ";") {
		return "", fmt.Errorf("%w: Codex routing model contains semicolon", ErrInvalidRequest)
	}
	if !httpguts.ValidHeaderFieldValue(model) {
		return "", fmt.Errorf("%w: Codex routing model is not a valid HTTP header field value", ErrInvalidRequest)
	}
	hint := "model=" + model
	if serviceTier != nil {
		hint += ";tier=" + string(*serviceTier)
	}
	if !httpguts.ValidHeaderFieldValue(hint) {
		return "", fmt.Errorf("%w: Codex routing hint is not a valid HTTP header field value", ErrInvalidRequest)
	}
	return hint, nil
}

func (f CodexDispatchFacts) validate() error {
	if strings.TrimSpace(f.SessionID) == "" {
		return fmt.Errorf("%w: Codex session ID is required", ErrInvalidRequest)
	}
	if strings.TrimSpace(f.RunID) == "" {
		return fmt.Errorf("%w: Codex run ID is required", ErrInvalidRequest)
	}
	if f.CompactionGeneration < 0 {
		return fmt.Errorf("%w: Codex compaction generation must be >= 0", ErrInvalidRequest)
	}
	if f.RequestKind == nil {
		return nil
	}
	switch *f.RequestKind {
	case CodexRequestKindTurn, CodexRequestKindCompaction:
		return nil
	default:
		return fmt.Errorf("%w: unknown Codex request kind %q", ErrInvalidRequest, *f.RequestKind)
	}
}
