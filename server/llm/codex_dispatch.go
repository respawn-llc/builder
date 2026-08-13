package llm

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"

	"golang.org/x/net/http/httpguts"
)

const maxCodexHeaderValueBytes = 8 * 1024

const codexTurnStateHeader = "x-codex-turn-state"

type CodexRequestKind string

const (
	CodexRequestKindTurn       CodexRequestKind = "turn"
	CodexRequestKindCompaction CodexRequestKind = "compaction"
)

type CodexDispatchFacts struct {
	SessionID            string
	RunID                string
	CompactionGeneration int
	RequestKind          CodexRequestKind
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
	CodexTurnStateDiagnosticInvalid  CodexTurnStateDiagnosticCategory = "provider_turn_state_invalid"
	CodexTurnStateDiagnosticConflict CodexTurnStateDiagnosticCategory = "provider_turn_state_conflict"
)

type codexTurnStateSource string

const (
	codexTurnStateSourceHTTPHeader codexTurnStateSource = "http_header"
	codexTurnStateSourceMetadata   codexTurnStateSource = "response_metadata"
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

func (c *CodexDispatchContext) Fresh() (*CodexDispatchContext, error) {
	if c == nil {
		return nil, fmt.Errorf("%w: Codex dispatch context is required", ErrInvalidRequest)
	}
	return NewCodexDispatchContext(c.facts)
}

func (c *CodexDispatchContext) TurnMetadataJSON() (string, error) {
	if err := c.validate(); err != nil {
		return "", err
	}
	var requestKind *CodexRequestKind
	if c.facts.RequestKind != "" {
		kind := c.facts.RequestKind
		requestKind = &kind
	}
	payload, err := json.Marshal(codexTurnMetadata{
		SessionID:   c.facts.SessionID,
		ThreadID:    c.facts.SessionID,
		TurnID:      c.facts.RunID,
		WindowID:    fmt.Sprintf("%s:%d", c.facts.SessionID, c.facts.CompactionGeneration),
		RequestKind: requestKind,
	})
	if err != nil {
		return "", fmt.Errorf("marshal Codex turn metadata: %w", err)
	}
	return string(payload), nil
}

func (c *CodexDispatchContext) SameState(other *CodexDispatchContext) bool {
	return c != nil && other != nil && c.state == other.state
}

func (c *CodexDispatchContext) outboundProjection(model string, serviceTier string) (codexDispatchProjection, error) {
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

func (s *CodexDispatchState) acceptTurnState(value string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.turnState = value
	s.hasTurnState = true
}

func (c *CodexDispatchContext) turnStateForRetry() string {
	value, ok := c.currentTurnState()
	if !ok {
		return ""
	}
	return value
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
	category := CodexTurnStateDiagnosticCategory("")
	c.state.mu.Lock()
	switch {
	case validateCodexHeaderValue(value) != nil:
		category = CodexTurnStateDiagnosticInvalid
	case !c.state.hasTurnState:
		c.state.turnState = value
		c.state.hasTurnState = true
	case c.state.turnState != value:
		category = CodexTurnStateDiagnosticConflict
	}
	shouldWarn := c.state.recordDiagnosticLocked(category)
	requestKind := c.facts.RequestKind
	c.state.mu.Unlock()

	if shouldWarn {
		slog.Warn(
			"ignored unusable provider turn state",
			"category", category,
			"source", source,
			"request_kind", requestKind,
		)
	}
}

func (c *CodexDispatchContext) observeInvalidTurnStateContainer(source codexTurnStateSource) {
	if c == nil || c.state == nil {
		return
	}
	c.state.mu.Lock()
	shouldWarn := c.state.recordDiagnosticLocked(CodexTurnStateDiagnosticInvalid)
	requestKind := c.facts.RequestKind
	c.state.mu.Unlock()
	if shouldWarn {
		slog.Warn(
			"ignored unusable provider turn state",
			"category", CodexTurnStateDiagnosticInvalid,
			"source", source,
			"request_kind", requestKind,
		)
	}
}

func (s *CodexDispatchState) recordDiagnosticLocked(category CodexTurnStateDiagnosticCategory) bool {
	if category == "" {
		return false
	}
	if s.diagnostics == nil {
		s.diagnostics = make(map[CodexTurnStateDiagnosticCategory]struct{}, 2)
	}
	if _, exists := s.diagnostics[category]; exists {
		return false
	}
	s.diagnostics[category] = struct{}{}
	return true
}

func (c *CodexDispatchContext) TurnStateDiagnostics() []CodexTurnStateDiagnosticCategory {
	if c == nil || c.state == nil {
		return nil
	}
	c.state.mu.Lock()
	defer c.state.mu.Unlock()
	result := make([]CodexTurnStateDiagnosticCategory, 0, len(c.state.diagnostics))
	for _, category := range []CodexTurnStateDiagnosticCategory{
		CodexTurnStateDiagnosticInvalid,
		CodexTurnStateDiagnosticConflict,
	} {
		if _, exists := c.state.diagnostics[category]; exists {
			result = append(result, category)
		}
	}
	return result
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

func validateOpenAIDispatchForMode(
	sessionID string,
	model string,
	dispatch *CodexDispatchContext,
	mode OpenAIAuthMode,
	serviceTier string,
) (codexDispatchProjection, error) {
	if !mode.IsOAuth {
		return codexDispatchProjection{}, nil
	}
	if dispatch == nil {
		return codexDispatchProjection{}, fmt.Errorf("%w: Codex dispatch context is required for OAuth dispatch", ErrInvalidRequest)
	}
	if err := dispatch.validateForSession(sessionID); err != nil {
		return codexDispatchProjection{}, err
	}
	return dispatch.outboundProjection(model, serviceTier)
}

func validateCodexHeaderValue(value string) error {
	if len(value) == 0 {
		return fmt.Errorf("value is required")
	}
	if len(value) > maxCodexHeaderValueBytes {
		return fmt.Errorf("value exceeds %d bytes", maxCodexHeaderValueBytes)
	}
	if value[0] == ' ' || value[0] == '\t' || value[len(value)-1] == ' ' || value[len(value)-1] == '\t' {
		return fmt.Errorf("value has leading or trailing SP/HTAB")
	}
	if !httpguts.ValidHeaderFieldValue(value) {
		return fmt.Errorf("value is not a valid HTTP header field value")
	}
	return nil
}

func buildCodexRoutingHint(model string, serviceTier string) (string, error) {
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
	if serviceTier != "" {
		hint += ";tier=" + serviceTier
	}
	if err := validateCodexHeaderValue(hint); err != nil {
		return "", fmt.Errorf("%w: Codex routing hint is not wire-realizable: %v", ErrInvalidRequest, err)
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
	switch f.RequestKind {
	case "", CodexRequestKindTurn, CodexRequestKindCompaction:
		return nil
	default:
		return fmt.Errorf("%w: unknown Codex request kind %q", ErrInvalidRequest, f.RequestKind)
	}
}
