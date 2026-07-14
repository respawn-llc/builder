package blackbox

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	"core/internal/testharness/pty/analyzer"

	"github.com/google/uuid"
)

const (
	scenarioVersion    = 1
	maxScenarioBytes   = 256 * 1024
	maxScenarioActions = 256
	maxModelOperations = 128
	maxScenarioPayload = 64 * 1024
)

type Scenario struct {
	Version         int                 `json:"version"`
	ID              uuid.UUID           `json:"id"`
	Dimensions      Dimensions          `json:"dimensions"`
	ModelOperations []RequiredOperation `json:"model_operations"`
	Actions         []Action            `json:"actions"`
}

type Dimensions struct {
	Rows int `json:"rows"`
	Cols int `json:"cols"`
}

type RequiredOperation struct {
	ID              uuid.UUID      `json:"id"`
	Route           Route          `json:"route"`
	Probe           *string        `json:"probe,omitempty"`
	Outcome         Outcome        `json:"outcome"`
	Output          *string        `json:"output,omitempty"`
	ResponsePhase   *ResponsePhase `json:"response_phase,omitempty"`
	SessionCacheKey bool           `json:"session_cache_key"`
}

type ResponsePhase string

const (
	ResponsePhaseAbsent     ResponsePhase = "absent"
	ResponsePhaseCommentary ResponsePhase = "commentary"
	ResponsePhaseFinal      ResponsePhase = "final_answer"
)

func NewResponsePhase(phase ResponsePhase) *ResponsePhase {
	return &phase
}

type Outcome string

const (
	OutcomeJSON            Outcome = "json"
	OutcomeStream          Outcome = "stream"
	OutcomeProviderFailure Outcome = "provider_failure"
	OutcomeHoldSSE         Outcome = "hold_sse"
)

type Route string

const (
	RouteResponses   Route = "responses"
	RouteCompact     Route = "compact"
	RouteInputTokens Route = "input_tokens"
	RouteModel       Route = "model_metadata"
)

type Action struct {
	ID         uuid.UUID   `json:"id"`
	Kind       ActionKind  `json:"kind"`
	Input      *string     `json:"input,omitempty"`
	Dimensions *Dimensions `json:"dimensions,omitempty"`
	Predicate  *Predicate  `json:"predicate,omitempty"`
}

type ActionKind string

const (
	ActionWait         ActionKind = "wait"
	ActionEnterInput   ActionKind = "enter_input"
	ActionSubmitPrompt ActionKind = "submit_prompt"
	ActionResize       ActionKind = "resize"
	ActionAssert       ActionKind = "assert"
	ActionCancel       ActionKind = "runtime_cancel"
	ActionTerminate    ActionKind = "terminate_process"
	ActionWaitExit     ActionKind = "wait_exit"
)

type Predicate struct {
	Kind     PredicateKind `json:"kind"`
	Rows     *int          `json:"rows,omitempty"`
	Cols     *int          `json:"cols,omitempty"`
	Enabled  *bool         `json:"enabled,omitempty"`
	Mode     *int          `json:"mode,omitempty"`
	Children []Predicate   `json:"children,omitempty"`
}

type PredicateKind string

const (
	PredicateParseable      PredicateKind = "parseable"
	PredicateBlank          PredicateKind = "blank"
	PredicateNonBlank       PredicateKind = "non_blank"
	PredicateDimensions     PredicateKind = "dimensions"
	PredicatePrivateMode    PredicateKind = "private_mode"
	PredicatePromptReady    PredicateKind = "prompt_ready"
	PredicateProcessExited  PredicateKind = "process_exited"
	PredicateServerReady    PredicateKind = "server_ready"
	PredicateModelConsumed  PredicateKind = "model_consumed"
	PredicateNoActiveModels PredicateKind = "no_active_models"
	PredicateAll            PredicateKind = "all"
	PredicateAny            PredicateKind = "any"
)

func LoadScenario(path string) (Scenario, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Scenario{}, fmt.Errorf("read scenario: %w", err)
	}
	return DecodeScenario(data)
}

func DecodeScenario(data []byte) (Scenario, error) {
	if len(data) > maxScenarioBytes {
		return Scenario{}, fmt.Errorf("scenario exceeds %d bytes", maxScenarioBytes)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var scenario Scenario
	if err := decoder.Decode(&scenario); err != nil {
		return Scenario{}, fmt.Errorf("decode scenario: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return Scenario{}, errors.New("scenario must contain one JSON document")
	}
	if err := scenario.Validate(); err != nil {
		return Scenario{}, err
	}
	return scenario, nil
}

func (s Scenario) Validate() error {
	if s.Version != scenarioVersion {
		return fmt.Errorf("unsupported scenario version %d", s.Version)
	}
	if err := validateV4(s.ID, "scenario id"); err != nil {
		return err
	}
	if _, err := analyzer.NewDimensions(s.Dimensions.Rows, s.Dimensions.Cols); err != nil {
		return fmt.Errorf("scenario dimensions: %w", err)
	}
	if len(s.Actions) > maxScenarioActions || len(s.ModelOperations) > maxModelOperations {
		return errors.New("scenario action or model operation limit exceeded")
	}
	seen := map[uuid.UUID]struct{}{s.ID: {}}
	for index, operation := range s.ModelOperations {
		if err := operation.Validate(); err != nil {
			return fmt.Errorf("model operation %d: %w", index, err)
		}
		if _, exists := seen[operation.ID]; exists {
			return fmt.Errorf("duplicate scenario identity %s", operation.ID)
		}
		seen[operation.ID] = struct{}{}
	}
	for index, action := range s.Actions {
		if err := action.Validate(); err != nil {
			return fmt.Errorf("action %d: %w", index, err)
		}
		if _, exists := seen[action.ID]; exists {
			return fmt.Errorf("duplicate scenario identity %s", action.ID)
		}
		seen[action.ID] = struct{}{}
	}
	return nil
}

func (operation RequiredOperation) Validate() error {
	if err := validateV4(operation.ID, "model operation id"); err != nil {
		return err
	}
	switch operation.Route {
	case RouteResponses, RouteCompact, RouteInputTokens, RouteModel:
	default:
		return fmt.Errorf("unsupported model route %q", operation.Route)
	}
	if operation.Probe != nil && len(*operation.Probe) > maxScenarioPayload {
		return errors.New("model probe exceeds limit")
	}
	if operation.Output != nil && len(*operation.Output) > maxScenarioPayload {
		return errors.New("model payload exceeds limit")
	}
	if operation.Route != RouteResponses && (operation.Probe != nil || operation.Output != nil || operation.ResponsePhase != nil || operation.SessionCacheKey || operation.Outcome == OutcomeStream || operation.Outcome == OutcomeHoldSSE) {
		return errors.New("only responses operations may declare probe, output, session cache key, stream, or hold outcome")
	}
	emitsAssistantMessage := operation.Route == RouteResponses && (operation.Output != nil || operation.Outcome == OutcomeStream)
	if emitsAssistantMessage {
		if operation.ResponsePhase == nil {
			return errors.New("responses operation emitting an assistant message requires response_phase")
		}
		switch *operation.ResponsePhase {
		case ResponsePhaseAbsent, ResponsePhaseCommentary, ResponsePhaseFinal:
		default:
			return errors.New("responses operation emitting an assistant message requires a valid response_phase")
		}
	} else if operation.ResponsePhase != nil {
		return errors.New("response_phase requires an emitted assistant message")
	}
	if operation.Probe != nil {
		probe, err := uuid.Parse(*operation.Probe)
		if err != nil || probe.Version() != 4 || probe == uuid.Nil {
			return errors.New("response probe must be UUIDv4")
		}
	}
	switch operation.Outcome {
	case OutcomeJSON, OutcomeStream, OutcomeProviderFailure, OutcomeHoldSSE:
	default:
		return fmt.Errorf("unsupported model outcome %q", operation.Outcome)
	}
	return nil
}

func (operation *RequiredOperation) UnmarshalJSON(data []byte) error {
	var raw struct {
		ID              uuid.UUID      `json:"id"`
		Route           Route          `json:"route"`
		Probe           *string        `json:"probe"`
		Outcome         Outcome        `json:"outcome"`
		Output          *string        `json:"output"`
		ResponsePhase   *ResponsePhase `json:"response_phase"`
		SessionCacheKey bool           `json:"session_cache_key"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	allowed := map[string]struct{}{"id": {}, "route": {}, "output": {}}
	if raw.Route == RouteResponses {
		allowed["probe"] = struct{}{}
		allowed["response_phase"] = struct{}{}
		allowed["session_cache_key"] = struct{}{}
	}
	allowed["outcome"] = struct{}{}
	if err := rejectUnknownJSONFields(data, allowed); err != nil {
		return err
	}
	*operation = RequiredOperation{
		ID:              raw.ID,
		Route:           raw.Route,
		Probe:           raw.Probe,
		Outcome:         raw.Outcome,
		Output:          raw.Output,
		ResponsePhase:   raw.ResponsePhase,
		SessionCacheKey: raw.SessionCacheKey,
	}
	return nil
}

func (action Action) Validate() error {
	if err := validateV4(action.ID, "action id"); err != nil {
		return err
	}
	if action.Input != nil && len(*action.Input) > maxScenarioPayload {
		return errors.New("action input exceeds limit")
	}
	switch action.Kind {
	case ActionWait, ActionAssert:
		if action.Predicate == nil || action.Input != nil || action.Dimensions != nil {
			return errors.New("predicate action must contain only a predicate")
		}
		return action.Predicate.Validate()
	case ActionEnterInput:
		if action.Input == nil || *action.Input == "" || action.Predicate != nil || action.Dimensions != nil {
			return errors.New("enter_input must contain only nonempty input")
		}
	case ActionSubmitPrompt, ActionCancel, ActionTerminate, ActionWaitExit:
		if action.Input != nil || action.Predicate != nil || action.Dimensions != nil {
			return fmt.Errorf("%s must not include payload", action.Kind)
		}
	case ActionResize:
		if action.Input != nil || action.Predicate != nil || action.Dimensions == nil {
			return errors.New("resize must contain only dimensions")
		}
		if _, err := analyzer.NewDimensions(action.Dimensions.Rows, action.Dimensions.Cols); err != nil {
			return err
		}
	default:
		return fmt.Errorf("unsupported action kind %q", action.Kind)
	}
	return nil
}

func (action *Action) UnmarshalJSON(data []byte) error {
	var raw struct {
		ID         uuid.UUID   `json:"id"`
		Kind       ActionKind  `json:"kind"`
		Input      *string     `json:"input"`
		Dimensions *Dimensions `json:"dimensions"`
		Predicate  *Predicate  `json:"predicate"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	allowed := map[string]struct{}{"id": {}, "kind": {}}
	switch raw.Kind {
	case ActionWait, ActionAssert:
		allowed["predicate"] = struct{}{}
	case ActionEnterInput:
		allowed["input"] = struct{}{}
	case ActionResize:
		allowed["dimensions"] = struct{}{}
	case ActionSubmitPrompt, ActionCancel, ActionTerminate, ActionWaitExit:
	default:
		return fmt.Errorf("unsupported action kind %q", raw.Kind)
	}
	if err := rejectUnknownJSONFields(data, allowed); err != nil {
		return err
	}
	if raw.Kind == ActionEnterInput && raw.Input == nil {
		return errors.New("enter_input input is required")
	}
	if (raw.Kind == ActionWait || raw.Kind == ActionAssert) && raw.Predicate == nil {
		return errors.New("predicate action predicate is required")
	}
	if raw.Kind == ActionResize && raw.Dimensions == nil {
		return errors.New("resize dimensions are required")
	}
	*action = Action{ID: raw.ID, Kind: raw.Kind, Input: raw.Input, Dimensions: raw.Dimensions, Predicate: raw.Predicate}
	return nil
}

func (predicate Predicate) Validate() error {
	switch predicate.Kind {
	case PredicateParseable, PredicateBlank, PredicateNonBlank, PredicatePromptReady, PredicateProcessExited, PredicateServerReady, PredicateModelConsumed, PredicateNoActiveModels:
		if predicate.Rows != nil || predicate.Cols != nil || predicate.Mode != nil || predicate.Enabled != nil || len(predicate.Children) != 0 {
			return fmt.Errorf("predicate %s includes irrelevant fields", predicate.Kind)
		}
	case PredicateDimensions:
		if predicate.Mode != nil || predicate.Enabled != nil || len(predicate.Children) != 0 {
			return errors.New("dimensions predicate includes irrelevant fields")
		}
		if predicate.Rows == nil || predicate.Cols == nil {
			return errors.New("dimensions predicate requires rows and cols")
		}
		if _, err := analyzer.NewDimensions(*predicate.Rows, *predicate.Cols); err != nil {
			return err
		}
	case PredicatePrivateMode:
		if predicate.Rows != nil || predicate.Cols != nil || len(predicate.Children) != 0 {
			return errors.New("private_mode predicate includes irrelevant fields")
		}
		if predicate.Mode == nil || *predicate.Mode <= 0 || predicate.Enabled == nil {
			return errors.New("private_mode requires positive mode and enabled")
		}
	case PredicateAll, PredicateAny:
		if predicate.Rows != nil || predicate.Cols != nil || predicate.Mode != nil || predicate.Enabled != nil {
			return errors.New("composite predicate includes irrelevant fields")
		}
		if len(predicate.Children) == 0 {
			return errors.New("composite predicate requires children")
		}
		for _, child := range predicate.Children {
			if err := child.Validate(); err != nil {
				return err
			}
		}
	default:
		return fmt.Errorf("unsupported predicate kind %q", predicate.Kind)
	}
	return nil
}

func (predicate *Predicate) UnmarshalJSON(data []byte) error {
	var raw struct {
		Kind     PredicateKind `json:"kind"`
		Rows     *int          `json:"rows"`
		Cols     *int          `json:"cols"`
		Enabled  *bool         `json:"enabled"`
		Mode     *int          `json:"mode"`
		Children []Predicate   `json:"children"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	allowed := map[string]struct{}{"kind": {}}
	switch raw.Kind {
	case PredicateParseable, PredicateBlank, PredicateNonBlank, PredicatePromptReady, PredicateProcessExited, PredicateServerReady, PredicateModelConsumed, PredicateNoActiveModels:
	case PredicateDimensions:
		allowed["rows"] = struct{}{}
		allowed["cols"] = struct{}{}
	case PredicatePrivateMode:
		allowed["mode"] = struct{}{}
		allowed["enabled"] = struct{}{}
	case PredicateAll, PredicateAny:
		allowed["children"] = struct{}{}
	default:
		return fmt.Errorf("unsupported predicate kind %q", raw.Kind)
	}
	if err := rejectUnknownJSONFields(data, allowed); err != nil {
		return err
	}
	*predicate = Predicate{
		Kind: raw.Kind, Rows: raw.Rows, Cols: raw.Cols, Enabled: raw.Enabled, Mode: raw.Mode, Children: raw.Children,
	}
	return nil
}

func rejectUnknownJSONFields(data []byte, allowed map[string]struct{}) error {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(data, &object); err != nil {
		return err
	}
	for field := range object {
		if _, ok := allowed[field]; !ok {
			return fmt.Errorf("field %q is not valid for this tagged union variant", field)
		}
	}
	return nil
}

func validateV4(value uuid.UUID, name string) error {
	if value == uuid.Nil || value.Version() != 4 {
		return fmt.Errorf("%s must be UUIDv4", name)
	}
	return nil
}
