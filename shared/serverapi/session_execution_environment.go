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

type SessionExecutionWorkspaceField struct {
	state rpcwire.FieldResult[SessionExecutionWorkspace, SessionExecutionWorkspaceUnavailableReason, SessionExecutionFieldError]
}
type SessionExecutionBranchField struct {
	state rpcwire.FieldResult[SessionExecutionBranch, SessionExecutionBranchUnavailableReason, SessionExecutionFieldError]
}
type SessionExecutionAuthField struct {
	state rpcwire.FieldResult[SessionExecutionAuth, SessionExecutionAuthUnavailableReason, SessionExecutionFieldError]
}
type SessionExecutionModelField struct {
	state rpcwire.FieldResult[SessionExecutionModel, SessionExecutionModelUnavailableReason, SessionExecutionFieldError]
}

func AvailableSessionExecutionWorkspace(path string) SessionExecutionWorkspaceField {
	return SessionExecutionWorkspaceField{state: rpcwire.AvailableField[
		SessionExecutionWorkspace,
		SessionExecutionWorkspaceUnavailableReason,
		SessionExecutionFieldError,
	](SessionExecutionWorkspace{Path: path})}
}
func FailedSessionExecutionWorkspace(err SessionExecutionFieldError) SessionExecutionWorkspaceField {
	return SessionExecutionWorkspaceField{state: rpcwire.FailedField[
		SessionExecutionWorkspace,
		SessionExecutionWorkspaceUnavailableReason,
	](err)}
}
func UnavailableSessionExecutionWorkspace(reason SessionExecutionWorkspaceUnavailableReason) SessionExecutionWorkspaceField {
	return SessionExecutionWorkspaceField{state: rpcwire.UnavailableField[
		SessionExecutionWorkspace,
		SessionExecutionWorkspaceUnavailableReason,
		SessionExecutionFieldError,
	](reason)}
}
func UnavailableSessionExecutionBranch(reason SessionExecutionBranchUnavailableReason) SessionExecutionBranchField {
	return SessionExecutionBranchField{state: rpcwire.UnavailableField[
		SessionExecutionBranch,
		SessionExecutionBranchUnavailableReason,
		SessionExecutionFieldError,
	](reason)}
}
func AvailableSessionExecutionBranch(name string) SessionExecutionBranchField {
	return SessionExecutionBranchField{state: rpcwire.AvailableField[
		SessionExecutionBranch,
		SessionExecutionBranchUnavailableReason,
		SessionExecutionFieldError,
	](SessionExecutionBranch{Name: name})}
}
func FailedSessionExecutionBranch(err SessionExecutionFieldError) SessionExecutionBranchField {
	return SessionExecutionBranchField{state: rpcwire.FailedField[
		SessionExecutionBranch,
		SessionExecutionBranchUnavailableReason,
	](err)}
}
func AvailableSessionExecutionAuth(value SessionExecutionAuth) SessionExecutionAuthField {
	return SessionExecutionAuthField{state: rpcwire.AvailableField[
		SessionExecutionAuth,
		SessionExecutionAuthUnavailableReason,
		SessionExecutionFieldError,
	](value)}
}
func UnavailableSessionExecutionAuth(reason SessionExecutionAuthUnavailableReason) SessionExecutionAuthField {
	return SessionExecutionAuthField{state: rpcwire.UnavailableField[
		SessionExecutionAuth,
		SessionExecutionAuthUnavailableReason,
		SessionExecutionFieldError,
	](reason)}
}
func FailedSessionExecutionAuth(err SessionExecutionFieldError) SessionExecutionAuthField {
	return SessionExecutionAuthField{state: rpcwire.FailedField[
		SessionExecutionAuth,
		SessionExecutionAuthUnavailableReason,
	](err)}
}
func AvailableSessionExecutionModel(value SessionExecutionModel) SessionExecutionModelField {
	return SessionExecutionModelField{state: rpcwire.AvailableField[
		SessionExecutionModel,
		SessionExecutionModelUnavailableReason,
		SessionExecutionFieldError,
	](value)}
}
func UnavailableSessionExecutionModel(reason SessionExecutionModelUnavailableReason) SessionExecutionModelField {
	return SessionExecutionModelField{state: rpcwire.UnavailableField[
		SessionExecutionModel,
		SessionExecutionModelUnavailableReason,
		SessionExecutionFieldError,
	](reason)}
}
func FailedSessionExecutionModel(err SessionExecutionFieldError) SessionExecutionModelField {
	return SessionExecutionModelField{state: rpcwire.FailedField[
		SessionExecutionModel,
		SessionExecutionModelUnavailableReason,
	](err)}
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

func (f SessionExecutionWorkspaceField) Kind() SessionExecutionFieldKind {
	return sessionExecutionFieldKind[SessionExecutionWorkspace, SessionExecutionWorkspaceUnavailableReason](f.state)
}
func (f SessionExecutionBranchField) Kind() SessionExecutionFieldKind {
	return sessionExecutionFieldKind[SessionExecutionBranch, SessionExecutionBranchUnavailableReason](f.state)
}
func (f SessionExecutionAuthField) Kind() SessionExecutionFieldKind {
	return sessionExecutionFieldKind[SessionExecutionAuth, SessionExecutionAuthUnavailableReason](f.state)
}
func (f SessionExecutionModelField) Kind() SessionExecutionFieldKind {
	return sessionExecutionFieldKind[SessionExecutionModel, SessionExecutionModelUnavailableReason](f.state)
}

func (f SessionExecutionWorkspaceField) Value() (SessionExecutionWorkspace, bool) {
	return rpcwire.FieldValue[SessionExecutionWorkspace, SessionExecutionWorkspaceUnavailableReason, SessionExecutionFieldError](f.state)
}
func (f SessionExecutionBranchField) Value() (SessionExecutionBranch, bool) {
	return rpcwire.FieldValue[SessionExecutionBranch, SessionExecutionBranchUnavailableReason, SessionExecutionFieldError](f.state)
}
func (f SessionExecutionAuthField) Value() (SessionExecutionAuth, bool) {
	return rpcwire.FieldValue[SessionExecutionAuth, SessionExecutionAuthUnavailableReason, SessionExecutionFieldError](f.state)
}
func (f SessionExecutionModelField) Value() (SessionExecutionModel, bool) {
	return rpcwire.FieldValue[SessionExecutionModel, SessionExecutionModelUnavailableReason, SessionExecutionFieldError](f.state)
}

func (f SessionExecutionWorkspaceField) UnavailableReason() (SessionExecutionWorkspaceUnavailableReason, bool) {
	return rpcwire.FieldUnavailableReason[SessionExecutionWorkspace, SessionExecutionWorkspaceUnavailableReason, SessionExecutionFieldError](f.state)
}
func (f SessionExecutionBranchField) UnavailableReason() (SessionExecutionBranchUnavailableReason, bool) {
	return rpcwire.FieldUnavailableReason[SessionExecutionBranch, SessionExecutionBranchUnavailableReason, SessionExecutionFieldError](f.state)
}
func (f SessionExecutionAuthField) UnavailableReason() (SessionExecutionAuthUnavailableReason, bool) {
	return rpcwire.FieldUnavailableReason[SessionExecutionAuth, SessionExecutionAuthUnavailableReason, SessionExecutionFieldError](f.state)
}
func (f SessionExecutionModelField) UnavailableReason() (SessionExecutionModelUnavailableReason, bool) {
	return rpcwire.FieldUnavailableReason[SessionExecutionModel, SessionExecutionModelUnavailableReason, SessionExecutionFieldError](f.state)
}

func (f SessionExecutionWorkspaceField) Failure() (SessionExecutionFieldError, bool) {
	return rpcwire.FieldFailure[SessionExecutionWorkspace, SessionExecutionWorkspaceUnavailableReason, SessionExecutionFieldError](f.state)
}
func (f SessionExecutionBranchField) Failure() (SessionExecutionFieldError, bool) {
	return rpcwire.FieldFailure[SessionExecutionBranch, SessionExecutionBranchUnavailableReason, SessionExecutionFieldError](f.state)
}
func (f SessionExecutionAuthField) Failure() (SessionExecutionFieldError, bool) {
	return rpcwire.FieldFailure[SessionExecutionAuth, SessionExecutionAuthUnavailableReason, SessionExecutionFieldError](f.state)
}
func (f SessionExecutionModelField) Failure() (SessionExecutionFieldError, bool) {
	return rpcwire.FieldFailure[SessionExecutionModel, SessionExecutionModelUnavailableReason, SessionExecutionFieldError](f.state)
}

func (f SessionExecutionWorkspaceField) MarshalJSON() ([]byte, error) {
	return marshalSessionExecutionField("workspace", f.state, validateSessionExecutionWorkspace, validateSessionExecutionWorkspaceReason)
}
func (f SessionExecutionBranchField) MarshalJSON() ([]byte, error) {
	return marshalSessionExecutionField("branch", f.state, validateSessionExecutionBranch, validateSessionExecutionBranchReason)
}
func (f SessionExecutionAuthField) MarshalJSON() ([]byte, error) {
	return marshalSessionExecutionField("auth", f.state, validateSessionExecutionAuth, validateSessionExecutionAuthReason)
}
func (f SessionExecutionModelField) MarshalJSON() ([]byte, error) {
	return marshalSessionExecutionField("model", f.state, validateSessionExecutionModel, validateSessionExecutionModelReason)
}

type sessionExecutionFieldWire[T any, R ~string] struct {
	Kind   SessionExecutionFieldKind   `json:"kind"`
	Value  *T                          `json:"value,omitempty"`
	Reason *R                          `json:"reason,omitempty"`
	Error  *SessionExecutionFieldError `json:"error,omitempty"`
}

func marshalSessionExecutionField[T any, R ~string](
	label string,
	state rpcwire.FieldResult[T, R, SessionExecutionFieldError],
	validateValue func(T) error,
	validateReason func(R) error,
) ([]byte, error) {
	if err := validateSessionExecutionField(label, state, validateValue, validateReason); err != nil {
		return nil, err
	}
	wire := sessionExecutionFieldWire[T, R]{Kind: sessionExecutionFieldKind[T, R](state)}
	switch wire.Kind {
	case SessionExecutionFieldAvailable:
		value, ok := rpcwire.FieldValue[T, R, SessionExecutionFieldError](state)
		if !ok {
			panic(fmt.Sprintf("%s available field has no value", label))
		}
		wire.Value = &value
	case SessionExecutionFieldUnavailable:
		reason, ok := rpcwire.FieldUnavailableReason[T, R, SessionExecutionFieldError](state)
		if !ok {
			panic(fmt.Sprintf("%s unavailable field has no reason", label))
		}
		wire.Reason = &reason
	case SessionExecutionFieldFailed:
		failure, ok := rpcwire.FieldFailure[T, R, SessionExecutionFieldError](state)
		if !ok {
			panic(fmt.Sprintf("%s failed field has no failure", label))
		}
		wire.Error = &failure
	}
	return json.Marshal(wire)
}

func decodeSessionExecutionField[T any, R ~string](
	data []byte,
	label string,
	validateValue func(T) error,
	validateReason func(R) error,
) (rpcwire.FieldResult[T, R, SessionExecutionFieldError], error) {
	var wire sessionExecutionFieldWire[T, R]
	if err := decodeStrictJSON(data, &wire); err != nil {
		return nil, err
	}
	var members map[string]json.RawMessage
	if err := json.Unmarshal(data, &members); err != nil {
		return nil, err
	}
	valuePresent := members["value"] != nil
	reasonPresent := members["reason"] != nil
	errorPresent := members["error"] != nil

	var state rpcwire.FieldResult[T, R, SessionExecutionFieldError]
	switch wire.Kind {
	case SessionExecutionFieldAvailable:
		if !valuePresent || wire.Value == nil || reasonPresent || errorPresent {
			return nil, fmt.Errorf("%s available field payload is invalid", label)
		}
		state = rpcwire.AvailableField[T, R, SessionExecutionFieldError](*wire.Value)
	case SessionExecutionFieldUnavailable:
		if valuePresent || !reasonPresent || wire.Reason == nil || errorPresent {
			return nil, fmt.Errorf("%s unavailable field payload is invalid", label)
		}
		state = rpcwire.UnavailableField[T, R, SessionExecutionFieldError](*wire.Reason)
	case SessionExecutionFieldFailed:
		if valuePresent || reasonPresent || !errorPresent || wire.Error == nil {
			return nil, fmt.Errorf("%s failed field payload is invalid", label)
		}
		state = rpcwire.FailedField[T, R](*wire.Error)
	default:
		return nil, fmt.Errorf("%s field kind %q is invalid", label, wire.Kind)
	}
	if err := validateSessionExecutionField(label, state, validateValue, validateReason); err != nil {
		return nil, err
	}
	return state, nil
}

func (f *SessionExecutionWorkspaceField) UnmarshalJSON(data []byte) error {
	state, err := decodeSessionExecutionField(
		data,
		"workspace",
		validateSessionExecutionWorkspace,
		validateSessionExecutionWorkspaceReason,
	)
	if err != nil {
		return err
	}
	f.state = state
	return nil
}

func (f *SessionExecutionBranchField) UnmarshalJSON(data []byte) error {
	state, err := decodeSessionExecutionField(
		data,
		"branch",
		validateSessionExecutionBranch,
		validateSessionExecutionBranchReason,
	)
	if err != nil {
		return err
	}
	f.state = state
	return nil
}

func (f *SessionExecutionAuthField) UnmarshalJSON(data []byte) error {
	state, err := decodeSessionExecutionField(
		data,
		"auth",
		validateSessionExecutionAuth,
		validateSessionExecutionAuthReason,
	)
	if err != nil {
		return err
	}
	f.state = state
	return nil
}

func (f *SessionExecutionModelField) UnmarshalJSON(data []byte) error {
	state, err := decodeSessionExecutionField(
		data,
		"model",
		validateSessionExecutionModel,
		validateSessionExecutionModelReason,
	)
	if err != nil {
		return err
	}
	f.state = state
	return nil
}

func validateSessionExecutionField[T any, R ~string](
	label string,
	state rpcwire.FieldResult[T, R, SessionExecutionFieldError],
	validateValue func(T) error,
	validateReason func(R) error,
) error {
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
		if err := validateValue(value); err != nil {
			return fmt.Errorf("%s available value is invalid: %w", label, err)
		}
	case rpcwire.FieldResultUnavailable:
		reason, reasonOK := rpcwire.FieldUnavailableReason[T, R, SessionExecutionFieldError](state)
		if !reasonOK {
			panic(fmt.Sprintf("%s unavailable field has no reason", label))
		}
		if err := validateReason(reason); err != nil {
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
	if err := validateSessionExecutionField(
		"workspace",
		e.Workspace.state,
		validateSessionExecutionWorkspace,
		validateSessionExecutionWorkspaceReason,
	); err != nil {
		return err
	}
	if err := validateSessionExecutionField(
		"branch",
		e.Branch.state,
		validateSessionExecutionBranch,
		validateSessionExecutionBranchReason,
	); err != nil {
		return err
	}
	if err := validateSessionExecutionField(
		"auth",
		e.Auth.state,
		validateSessionExecutionAuth,
		validateSessionExecutionAuthReason,
	); err != nil {
		return err
	}
	return validateSessionExecutionField(
		"model",
		e.Model.state,
		validateSessionExecutionModel,
		validateSessionExecutionModelReason,
	)
}

func (e SessionExecutionEnvironment) MarshalJSON() ([]byte, error) {
	if err := e.Validate(); err != nil {
		return nil, err
	}
	type wire SessionExecutionEnvironment
	return json.Marshal(wire(e))
}

func (e *SessionExecutionEnvironment) UnmarshalJSON(data []byte) error {
	var wire struct {
		SessionID runtimeids.SessionID           `json:"session_id"`
		Workspace SessionExecutionWorkspaceField `json:"workspace"`
		Branch    SessionExecutionBranchField    `json:"branch"`
		Auth      SessionExecutionAuthField      `json:"auth"`
		Model     SessionExecutionModelField     `json:"model"`
	}
	if err := decodeStrictJSON(data, &wire); err != nil {
		return err
	}
	value := SessionExecutionEnvironment{SessionID: wire.SessionID, Workspace: wire.Workspace, Branch: wire.Branch, Auth: wire.Auth, Model: wire.Model}
	if err := value.Validate(); err != nil {
		return err
	}
	*e = value
	return nil
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

func (r *SessionExecutionEnvironmentRequest) UnmarshalJSON(data []byte) error {
	var wire struct {
		SessionID runtimeids.SessionID `json:"session_id"`
	}
	if err := decodeStrictJSON(data, &wire); err != nil {
		return err
	}
	value := SessionExecutionEnvironmentRequest{SessionID: wire.SessionID}
	if err := value.Validate(); err != nil {
		return err
	}
	*r = value
	return nil
}

func (r *SessionExecutionEnvironmentResponse) UnmarshalJSON(data []byte) error {
	var wire struct {
		Environment SessionExecutionEnvironment `json:"environment"`
	}
	if err := decodeStrictJSON(data, &wire); err != nil {
		return err
	}
	if err := wire.Environment.Validate(); err != nil {
		return err
	}
	r.Environment = wire.Environment
	return nil
}
func (r SessionExecutionEnvironmentResponse) Validate() error { return r.Environment.Validate() }
