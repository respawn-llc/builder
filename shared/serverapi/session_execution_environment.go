package serverapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"core/shared/rpcwire"
	"core/shared/runtimeids"
)

type SessionExecutionFieldKind string

const (
	SessionExecutionFieldAvailable   SessionExecutionFieldKind = "available"
	SessionExecutionFieldUnavailable SessionExecutionFieldKind = "unavailable"
	SessionExecutionFieldFailed      SessionExecutionFieldKind = "failed"
)

type SessionExecutionFieldErrorCode string

const (
	SessionExecutionFieldErrorSourceFailure        SessionExecutionFieldErrorCode = "source_failure"
	SessionExecutionFieldErrorInvalidConfiguration SessionExecutionFieldErrorCode = "invalid_configuration"
)

type SessionExecutionFieldError struct {
	Code    SessionExecutionFieldErrorCode `json:"code"`
	Message string                         `json:"message"`
}

func (e SessionExecutionFieldError) Validate() error {
	switch e.Code {
	case SessionExecutionFieldErrorSourceFailure, SessionExecutionFieldErrorInvalidConfiguration:
	default:
		return fmt.Errorf("session execution field error code %q is invalid", e.Code)
	}
	if strings.TrimSpace(e.Message) == "" {
		return errors.New("session execution field error message is required")
	}
	return nil
}

type SessionExecutionBranchUnavailableReason string

const (
	SessionExecutionBranchUnavailableDetachedHead     SessionExecutionBranchUnavailableReason = "detached_head"
	SessionExecutionBranchUnavailableNotGitRepository SessionExecutionBranchUnavailableReason = "not_git_repository"
)

type SessionExecutionWorkspaceUnavailableReason string

const (
	SessionExecutionWorkspaceUnavailableNotConfigured SessionExecutionWorkspaceUnavailableReason = "not_configured"
)

type SessionExecutionAuthUnavailableReason string

const (
	SessionExecutionAuthUnavailableNotApplicable SessionExecutionAuthUnavailableReason = "not_applicable"
)

type SessionExecutionModelUnavailableReason string

const (
	SessionExecutionModelUnavailableNotConfigured SessionExecutionModelUnavailableReason = "not_configured"
)

type SessionExecutionAuthMethod string

const (
	SessionExecutionAuthMethodNone   SessionExecutionAuthMethod = "none"
	SessionExecutionAuthMethodAPIKey SessionExecutionAuthMethod = "api_key"
	SessionExecutionAuthMethodOAuth  SessionExecutionAuthMethod = "oauth"
)

type SessionExecutionAuth struct {
	Provider string                     `json:"provider"`
	Method   SessionExecutionAuthMethod `json:"method"`
}

type SessionExecutionModel struct {
	Name     string `json:"name"`
	Provider string `json:"provider"`
	Locked   bool   `json:"locked"`
}

type SessionExecutionWorkspace struct {
	Path string `json:"path"`
}
type SessionExecutionBranch struct {
	Name string `json:"name"`
}

type sessionExecutionWorkspaceFieldShape struct{}
type sessionExecutionBranchFieldShape struct{}
type sessionExecutionAuthFieldShape struct{}
type sessionExecutionModelFieldShape struct{}

type SessionExecutionField[T any, R ~string, S rpcwire.FieldResultSchema[T, R]] struct {
	state rpcwire.FieldResult[T, R, SessionExecutionFieldError]
}

type SessionExecutionWorkspaceField = SessionExecutionField[
	SessionExecutionWorkspace,
	SessionExecutionWorkspaceUnavailableReason,
	sessionExecutionWorkspaceFieldShape,
]
type SessionExecutionBranchField = SessionExecutionField[
	SessionExecutionBranch,
	SessionExecutionBranchUnavailableReason,
	sessionExecutionBranchFieldShape,
]
type SessionExecutionAuthField = SessionExecutionField[
	SessionExecutionAuth,
	SessionExecutionAuthUnavailableReason,
	sessionExecutionAuthFieldShape,
]
type SessionExecutionModelField = SessionExecutionField[
	SessionExecutionModel,
	SessionExecutionModelUnavailableReason,
	sessionExecutionModelFieldShape,
]

func availableSessionExecutionField[T any, R ~string, S rpcwire.FieldResultSchema[T, R]](value T) SessionExecutionField[T, R, S] {
	return SessionExecutionField[T, R, S]{state: rpcwire.AvailableField[T, R, SessionExecutionFieldError](value)}
}

func unavailableSessionExecutionField[T any, R ~string, S rpcwire.FieldResultSchema[T, R]](reason R) SessionExecutionField[T, R, S] {
	return SessionExecutionField[T, R, S]{state: rpcwire.UnavailableField[T, R, SessionExecutionFieldError](reason)}
}

func failedSessionExecutionField[T any, R ~string, S rpcwire.FieldResultSchema[T, R]](failure SessionExecutionFieldError) SessionExecutionField[T, R, S] {
	return SessionExecutionField[T, R, S]{state: rpcwire.FailedField[T, R](failure)}
}

func AvailableSessionExecutionWorkspace(path string) SessionExecutionWorkspaceField {
	return availableSessionExecutionField[
		SessionExecutionWorkspace,
		SessionExecutionWorkspaceUnavailableReason,
		sessionExecutionWorkspaceFieldShape,
	](SessionExecutionWorkspace{Path: path})
}
func FailedSessionExecutionWorkspace(err SessionExecutionFieldError) SessionExecutionWorkspaceField {
	return failedSessionExecutionField[
		SessionExecutionWorkspace,
		SessionExecutionWorkspaceUnavailableReason,
		sessionExecutionWorkspaceFieldShape,
	](err)
}
func UnavailableSessionExecutionWorkspace(reason SessionExecutionWorkspaceUnavailableReason) SessionExecutionWorkspaceField {
	return unavailableSessionExecutionField[
		SessionExecutionWorkspace,
		SessionExecutionWorkspaceUnavailableReason,
		sessionExecutionWorkspaceFieldShape,
	](reason)
}
func UnavailableSessionExecutionBranch(reason SessionExecutionBranchUnavailableReason) SessionExecutionBranchField {
	return unavailableSessionExecutionField[
		SessionExecutionBranch,
		SessionExecutionBranchUnavailableReason,
		sessionExecutionBranchFieldShape,
	](reason)
}
func AvailableSessionExecutionBranch(name string) SessionExecutionBranchField {
	return availableSessionExecutionField[
		SessionExecutionBranch,
		SessionExecutionBranchUnavailableReason,
		sessionExecutionBranchFieldShape,
	](SessionExecutionBranch{Name: name})
}
func FailedSessionExecutionBranch(err SessionExecutionFieldError) SessionExecutionBranchField {
	return failedSessionExecutionField[
		SessionExecutionBranch,
		SessionExecutionBranchUnavailableReason,
		sessionExecutionBranchFieldShape,
	](err)
}
func AvailableSessionExecutionAuth(value SessionExecutionAuth) SessionExecutionAuthField {
	return availableSessionExecutionField[
		SessionExecutionAuth,
		SessionExecutionAuthUnavailableReason,
		sessionExecutionAuthFieldShape,
	](value)
}
func UnavailableSessionExecutionAuth(reason SessionExecutionAuthUnavailableReason) SessionExecutionAuthField {
	return unavailableSessionExecutionField[
		SessionExecutionAuth,
		SessionExecutionAuthUnavailableReason,
		sessionExecutionAuthFieldShape,
	](reason)
}
func FailedSessionExecutionAuth(err SessionExecutionFieldError) SessionExecutionAuthField {
	return failedSessionExecutionField[
		SessionExecutionAuth,
		SessionExecutionAuthUnavailableReason,
		sessionExecutionAuthFieldShape,
	](err)
}
func AvailableSessionExecutionModel(value SessionExecutionModel) SessionExecutionModelField {
	return availableSessionExecutionField[
		SessionExecutionModel,
		SessionExecutionModelUnavailableReason,
		sessionExecutionModelFieldShape,
	](value)
}
func UnavailableSessionExecutionModel(reason SessionExecutionModelUnavailableReason) SessionExecutionModelField {
	return unavailableSessionExecutionField[
		SessionExecutionModel,
		SessionExecutionModelUnavailableReason,
		sessionExecutionModelFieldShape,
	](reason)
}
func FailedSessionExecutionModel(err SessionExecutionFieldError) SessionExecutionModelField {
	return failedSessionExecutionField[
		SessionExecutionModel,
		SessionExecutionModelUnavailableReason,
		sessionExecutionModelFieldShape,
	](err)
}

func sessionExecutionFieldKind[T any, R ~string](
	state rpcwire.FieldResult[T, R, SessionExecutionFieldError],
) SessionExecutionFieldKind {
	kind, ok := rpcwire.FieldResultKindOf[T, R, SessionExecutionFieldError](state)
	if !ok {
		panic("session execution field kind requires a state")
	}
	switch kind {
	case rpcwire.FieldResultAvailable:
		return SessionExecutionFieldAvailable
	case rpcwire.FieldResultUnavailable:
		return SessionExecutionFieldUnavailable
	case rpcwire.FieldResultFailed:
		return SessionExecutionFieldFailed
	default:
		panic(fmt.Sprintf("unknown field result kind %d", kind))
	}
}

func (f SessionExecutionField[T, R, S]) Kind() SessionExecutionFieldKind {
	return sessionExecutionFieldKind[T, R](f.state)
}

func (f SessionExecutionField[T, R, S]) Value() (T, bool) {
	return rpcwire.FieldValue[T, R, SessionExecutionFieldError](f.state)
}

func (f SessionExecutionField[T, R, S]) UnavailableReason() (R, bool) {
	return rpcwire.FieldUnavailableReason[T, R, SessionExecutionFieldError](f.state)
}

func (f SessionExecutionField[T, R, S]) Failure() (SessionExecutionFieldError, bool) {
	return rpcwire.FieldFailure[T, R, SessionExecutionFieldError](f.state)
}

func (f SessionExecutionField[T, R, S]) validate() error {
	var shape S
	return validateSessionExecutionField(f.state, shape)
}

type sessionExecutionFieldWire[T any, R ~string] struct {
	Kind   SessionExecutionFieldKind   `json:"kind"`
	Value  *T                          `json:"value,omitempty"`
	Reason *R                          `json:"reason,omitempty"`
	Error  *SessionExecutionFieldError `json:"error,omitempty"`
}

func (f SessionExecutionField[T, R, S]) MarshalJSON() ([]byte, error) {
	if err := f.validate(); err != nil {
		return nil, err
	}
	var shape S
	label := shape.Label()
	wire := sessionExecutionFieldWire[T, R]{Kind: f.Kind()}
	switch wire.Kind {
	case SessionExecutionFieldAvailable:
		value, ok := f.Value()
		if !ok {
			panic(fmt.Sprintf("%s available field has no value", label))
		}
		wire.Value = &value
	case SessionExecutionFieldUnavailable:
		reason, ok := f.UnavailableReason()
		if !ok {
			panic(fmt.Sprintf("%s unavailable field has no reason", label))
		}
		wire.Reason = &reason
	case SessionExecutionFieldFailed:
		failure, ok := f.Failure()
		if !ok {
			panic(fmt.Sprintf("%s failed field has no failure", label))
		}
		wire.Error = &failure
	}
	return json.Marshal(wire)
}

func validateSessionExecutionField[T any, R ~string](
	state rpcwire.FieldResult[T, R, SessionExecutionFieldError],
	schema rpcwire.FieldResultSchema[T, R],
) error {
	label := schema.Label()
	kind, ok := rpcwire.FieldResultKindOf[T, R, SessionExecutionFieldError](state)
	if !ok {
		return fmt.Errorf("%s field state is required", label)
	}
	switch kind {
	case rpcwire.FieldResultAvailable:
		value, valueOK := rpcwire.FieldValue[T, R, SessionExecutionFieldError](state)
		if !valueOK {
			panic(fmt.Sprintf("%s available field has no value", label))
		}
		if err := schema.ValidateValue(value); err != nil {
			return fmt.Errorf("%s available value is invalid: %w", label, err)
		}
	case rpcwire.FieldResultUnavailable:
		reason, reasonOK := rpcwire.FieldUnavailableReason[T, R, SessionExecutionFieldError](state)
		if !reasonOK {
			panic(fmt.Sprintf("%s unavailable field has no reason", label))
		}
		if err := schema.ValidateUnavailableReason(reason); err != nil {
			return fmt.Errorf("%s unavailable reason is invalid: %w", label, err)
		}
	case rpcwire.FieldResultFailed:
		failure, failureOK := rpcwire.FieldFailure[T, R, SessionExecutionFieldError](state)
		if !failureOK {
			panic(fmt.Sprintf("%s failed field has no failure", label))
		}
		if err := failure.Validate(); err != nil {
			return fmt.Errorf("%s: %w", label, err)
		}
	default:
		panic(fmt.Sprintf("unknown field result kind %d", kind))
	}
	return nil
}

func validateSessionExecutionWorkspace(value SessionExecutionWorkspace) error {
	if strings.TrimSpace(value.Path) == "" {
		return errors.New("path is required")
	}
	return nil
}

func validateSessionExecutionBranch(value SessionExecutionBranch) error {
	if strings.TrimSpace(value.Name) == "" {
		return errors.New("name is required")
	}
	return nil
}

func validateSessionExecutionAuth(value SessionExecutionAuth) error {
	if strings.TrimSpace(value.Provider) == "" {
		return errors.New("provider is required")
	}
	switch value.Method {
	case SessionExecutionAuthMethodNone, SessionExecutionAuthMethodAPIKey, SessionExecutionAuthMethodOAuth:
		return nil
	default:
		return fmt.Errorf("method %q is invalid", value.Method)
	}
}

func validateSessionExecutionModel(value SessionExecutionModel) error {
	if strings.TrimSpace(value.Name) == "" {
		return errors.New("name is required")
	}
	if strings.TrimSpace(value.Provider) == "" {
		return errors.New("provider is required")
	}
	return nil
}

func validateSessionExecutionWorkspaceReason(reason SessionExecutionWorkspaceUnavailableReason) error {
	if reason != SessionExecutionWorkspaceUnavailableNotConfigured {
		return fmt.Errorf("%q is invalid", reason)
	}
	return nil
}

func validateSessionExecutionBranchReason(reason SessionExecutionBranchUnavailableReason) error {
	switch reason {
	case SessionExecutionBranchUnavailableDetachedHead, SessionExecutionBranchUnavailableNotGitRepository:
		return nil
	default:
		return fmt.Errorf("%q is invalid", reason)
	}
}

func validateSessionExecutionAuthReason(reason SessionExecutionAuthUnavailableReason) error {
	if reason != SessionExecutionAuthUnavailableNotApplicable {
		return fmt.Errorf("%q is invalid", reason)
	}
	return nil
}

func validateSessionExecutionModelReason(reason SessionExecutionModelUnavailableReason) error {
	if reason != SessionExecutionModelUnavailableNotConfigured {
		return fmt.Errorf("%q is invalid", reason)
	}
	return nil
}

func (sessionExecutionWorkspaceFieldShape) Label() string { return "workspace" }
func (sessionExecutionWorkspaceFieldShape) ValidateValue(value SessionExecutionWorkspace) error {
	return validateSessionExecutionWorkspace(value)
}
func (sessionExecutionWorkspaceFieldShape) ValidateUnavailableReason(reason SessionExecutionWorkspaceUnavailableReason) error {
	return validateSessionExecutionWorkspaceReason(reason)
}

func (sessionExecutionBranchFieldShape) Label() string { return "branch" }
func (sessionExecutionBranchFieldShape) ValidateValue(value SessionExecutionBranch) error {
	return validateSessionExecutionBranch(value)
}
func (sessionExecutionBranchFieldShape) ValidateUnavailableReason(reason SessionExecutionBranchUnavailableReason) error {
	return validateSessionExecutionBranchReason(reason)
}

func (sessionExecutionAuthFieldShape) Label() string { return "auth" }
func (sessionExecutionAuthFieldShape) ValidateValue(value SessionExecutionAuth) error {
	return validateSessionExecutionAuth(value)
}
func (sessionExecutionAuthFieldShape) ValidateUnavailableReason(reason SessionExecutionAuthUnavailableReason) error {
	return validateSessionExecutionAuthReason(reason)
}

func (sessionExecutionModelFieldShape) Label() string { return "model" }
func (sessionExecutionModelFieldShape) ValidateValue(value SessionExecutionModel) error {
	return validateSessionExecutionModel(value)
}
func (sessionExecutionModelFieldShape) ValidateUnavailableReason(reason SessionExecutionModelUnavailableReason) error {
	return validateSessionExecutionModelReason(reason)
}

type SessionExecutionEnvironment struct {
	SessionID runtimeids.SessionID           `json:"session_id"`
	Workspace SessionExecutionWorkspaceField `json:"workspace"`
	Branch    SessionExecutionBranchField    `json:"branch"`
	Auth      SessionExecutionAuthField      `json:"auth"`
	Model     SessionExecutionModelField     `json:"model"`
}

func (e SessionExecutionEnvironment) Validate() error {
	if e.SessionID.IsZero() {
		return errors.New("session execution environment session_id is required")
	}
	if err := e.Workspace.validate(); err != nil {
		return err
	}
	if err := e.Branch.validate(); err != nil {
		return err
	}
	if err := e.Auth.validate(); err != nil {
		return err
	}
	return e.Model.validate()
}

func (e SessionExecutionEnvironment) MarshalJSON() ([]byte, error) {
	if err := e.Validate(); err != nil {
		return nil, err
	}
	type wire SessionExecutionEnvironment
	return json.Marshal(wire(e))
}

type SessionExecutionEnvironmentRequest struct {
	SessionID runtimeids.SessionID `json:"session_id"`
}
type SessionExecutionEnvironmentResponse struct {
	Environment SessionExecutionEnvironment `json:"environment"`
}

func (r SessionExecutionEnvironmentResponse) MarshalJSON() ([]byte, error) {
	if err := r.Validate(); err != nil {
		return nil, err
	}
	type wire SessionExecutionEnvironmentResponse
	return json.Marshal(wire(r))
}

func (r SessionExecutionEnvironmentRequest) Validate() error {
	if r.SessionID.IsZero() {
		return errors.New("session_id is required")
	}
	return nil
}

func (r SessionExecutionEnvironmentResponse) Validate() error { return r.Environment.Validate() }
