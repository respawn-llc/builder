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
	ID              uuid.UUID `json:"id"`
	Route           Route     `json:"route"`
	Probe           string    `json:"probe,omitempty"`
	Stream          bool      `json:"stream"`
	Output          string    `json:"output,omitempty"`
	SessionCacheKey bool      `json:"session_cache_key"`
}

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
	Input      string      `json:"input,omitempty"`
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
	Rows     int           `json:"rows,omitempty"`
	Cols     int           `json:"cols,omitempty"`
	Enabled  *bool         `json:"enabled,omitempty"`
	Mode     int           `json:"mode,omitempty"`
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
	if len(operation.Probe) > maxScenarioPayload || len(operation.Output) > maxScenarioPayload {
		return errors.New("model payload exceeds limit")
	}
	if operation.Route != RouteResponses && (operation.Probe != "" || operation.Stream || operation.SessionCacheKey) {
		return errors.New("only responses operations may declare probe, stream, or session cache key")
	}
	if operation.Probe != "" {
		probe, err := uuid.Parse(operation.Probe)
		if err != nil || probe.Version() != 4 || probe == uuid.Nil {
			return errors.New("response probe must be UUIDv4")
		}
	}
	return nil
}

func (action Action) Validate() error {
	if err := validateV4(action.ID, "action id"); err != nil {
		return err
	}
	if len(action.Input) > maxScenarioPayload {
		return errors.New("action input exceeds limit")
	}
	switch action.Kind {
	case ActionWait, ActionAssert:
		if action.Predicate == nil || action.Input != "" || action.Dimensions != nil {
			return errors.New("predicate action must contain only a predicate")
		}
		return action.Predicate.Validate()
	case ActionEnterInput:
		if action.Input == "" || action.Predicate != nil || action.Dimensions != nil {
			return errors.New("enter_input must contain only nonempty input")
		}
	case ActionSubmitPrompt, ActionCancel, ActionTerminate, ActionWaitExit:
		if action.Input != "" || action.Predicate != nil || action.Dimensions != nil {
			return fmt.Errorf("%s must not include payload", action.Kind)
		}
	case ActionResize:
		if action.Input != "" || action.Predicate != nil || action.Dimensions == nil {
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

func (predicate Predicate) Validate() error {
	switch predicate.Kind {
	case PredicateParseable, PredicateBlank, PredicateNonBlank, PredicatePromptReady, PredicateProcessExited, PredicateServerReady, PredicateModelConsumed, PredicateNoActiveModels:
		if predicate.Rows != 0 || predicate.Cols != 0 || predicate.Mode != 0 || predicate.Enabled != nil || len(predicate.Children) != 0 {
			return fmt.Errorf("predicate %s includes irrelevant fields", predicate.Kind)
		}
	case PredicateDimensions:
		if _, err := analyzer.NewDimensions(predicate.Rows, predicate.Cols); err != nil {
			return err
		}
	case PredicatePrivateMode:
		if predicate.Mode <= 0 || predicate.Enabled == nil {
			return errors.New("private_mode requires positive mode and enabled")
		}
	case PredicateAll, PredicateAny:
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

func validateV4(value uuid.UUID, name string) error {
	if value == uuid.Nil || value.Version() != 4 {
		return fmt.Errorf("%s must be UUIDv4", name)
	}
	return nil
}
