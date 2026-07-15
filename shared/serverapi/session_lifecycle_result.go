package serverapi

import (
	"encoding/json"
	"errors"
	"strings"
)

type SessionAuthPreparation string

const (
	SessionAuthPreparationKeepCurrent    SessionAuthPreparation = "keep_current_auth"
	SessionAuthPreparationReauthenticate SessionAuthPreparation = "reauthenticate"
)

func (p SessionAuthPreparation) Validate() error {
	switch p {
	case SessionAuthPreparationKeepCurrent, SessionAuthPreparationReauthenticate:
		return nil
	default:
		return errors.New("session auth preparation is invalid")
	}
}

type SessionInitialPromptMetadata struct {
	Text            string `json:"text"`
	HistoryRecorded bool   `json:"history_recorded,omitempty"`
}

func (m SessionInitialPromptMetadata) Validate() error {
	if strings.TrimSpace(m.Text) == "" {
		return errors.New("session initial prompt text is required")
	}
	return nil
}

type SessionInitialInputPolicyKind string

const (
	SessionInitialInputPolicyRestoreStoredDraft  SessionInitialInputPolicyKind = "restore_stored_draft"
	SessionInitialInputPolicyOverrideStoredDraft SessionInitialInputPolicyKind = "override_stored_draft"
)

type SessionInitialInputPolicy struct {
	kind         SessionInitialInputPolicyKind
	overrideText *string
}

func RestoreStoredDraftSessionInitialInputPolicy() SessionInitialInputPolicy {
	return SessionInitialInputPolicy{kind: SessionInitialInputPolicyRestoreStoredDraft}
}

func OverrideStoredDraftSessionInitialInputPolicy(text string) SessionInitialInputPolicy {
	return SessionInitialInputPolicy{kind: SessionInitialInputPolicyOverrideStoredDraft, overrideText: &text}
}

func (p SessionInitialInputPolicy) Kind() SessionInitialInputPolicyKind {
	return p.kind
}

func (p SessionInitialInputPolicy) OverrideText() (string, bool) {
	if p.overrideText == nil {
		return "", false
	}
	return *p.overrideText, true
}

func (p SessionInitialInputPolicy) Equal(other SessionInitialInputPolicy) bool {
	if p.kind != other.kind {
		return false
	}
	left, leftPresent := p.OverrideText()
	right, rightPresent := other.OverrideText()
	return leftPresent == rightPresent && (!leftPresent || left == right)
}

func (p SessionInitialInputPolicy) Validate() error {
	switch p.kind {
	case SessionInitialInputPolicyRestoreStoredDraft:
		if p.overrideText != nil {
			return errors.New("restore_stored_draft input policy cannot contain text")
		}
	case SessionInitialInputPolicyOverrideStoredDraft:
		if p.overrideText == nil {
			return errors.New("override_stored_draft input policy requires text")
		}
	default:
		return errors.New("session initial input policy kind is invalid")
	}
	return nil
}

func (p SessionInitialInputPolicy) MarshalJSON() ([]byte, error) {
	if err := p.Validate(); err != nil {
		return nil, err
	}
	type wire struct {
		Kind SessionInitialInputPolicyKind `json:"kind"`
		Text *string                       `json:"text,omitempty"`
	}
	return json.Marshal(wire{Kind: p.kind, Text: p.overrideText})
}

func (p *SessionInitialInputPolicy) UnmarshalJSON(data []byte) error {
	var wire struct {
		Kind SessionInitialInputPolicyKind `json:"kind"`
		Text *string                       `json:"text"`
	}
	if err := decodeStrictJSON(data, &wire); err != nil {
		return err
	}
	decoded := SessionInitialInputPolicy{kind: wire.Kind, overrideText: wire.Text}
	if err := decoded.Validate(); err != nil {
		return err
	}
	*p = decoded
	return nil
}

type SessionLaunchPreparation struct {
	initialPrompt *SessionInitialPromptMetadata
	inputPolicy   SessionInitialInputPolicy
	auth          SessionAuthPreparation
}

func NewSessionLaunchPreparation(initialPrompt *SessionInitialPromptMetadata, inputPolicy SessionInitialInputPolicy, auth SessionAuthPreparation) SessionLaunchPreparation {
	preparation := SessionLaunchPreparation{inputPolicy: inputPolicy, auth: auth}
	if initialPrompt != nil {
		copied := *initialPrompt
		preparation.initialPrompt = &copied
	}
	return preparation
}

func (p SessionLaunchPreparation) InitialPrompt() (SessionInitialPromptMetadata, bool) {
	if p.initialPrompt == nil {
		return SessionInitialPromptMetadata{}, false
	}
	return *p.initialPrompt, true
}

func (p SessionLaunchPreparation) InitialInputPolicy() SessionInitialInputPolicy {
	return p.inputPolicy
}

func (p SessionLaunchPreparation) AuthPreparation() SessionAuthPreparation {
	return p.auth
}

func (p SessionLaunchPreparation) Equal(other SessionLaunchPreparation) bool {
	leftPrompt, leftHasPrompt := p.InitialPrompt()
	rightPrompt, rightHasPrompt := other.InitialPrompt()
	return leftHasPrompt == rightHasPrompt &&
		(!leftHasPrompt || leftPrompt == rightPrompt) &&
		p.inputPolicy.Equal(other.inputPolicy) &&
		p.auth == other.auth
}

func (p SessionLaunchPreparation) Validate() error {
	if p.initialPrompt != nil {
		if err := p.initialPrompt.Validate(); err != nil {
			return err
		}
	}
	if err := p.inputPolicy.Validate(); err != nil {
		return err
	}
	return p.auth.Validate()
}

func (p SessionLaunchPreparation) MarshalJSON() ([]byte, error) {
	if err := p.Validate(); err != nil {
		return nil, err
	}
	type wire struct {
		InitialPrompt *SessionInitialPromptMetadata `json:"initial_prompt,omitempty"`
		InputPolicy   SessionInitialInputPolicy     `json:"input_policy"`
		Auth          SessionAuthPreparation        `json:"auth"`
	}
	return json.Marshal(wire{
		InitialPrompt: p.initialPrompt,
		InputPolicy:   p.inputPolicy,
		Auth:          p.auth,
	})
}

func (p *SessionLaunchPreparation) UnmarshalJSON(data []byte) error {
	var wire struct {
		InitialPrompt *SessionInitialPromptMetadata `json:"initial_prompt"`
		InputPolicy   SessionInitialInputPolicy     `json:"input_policy"`
		Auth          SessionAuthPreparation        `json:"auth"`
	}
	if err := decodeStrictJSON(data, &wire); err != nil {
		return err
	}
	decoded := NewSessionLaunchPreparation(wire.InitialPrompt, wire.InputPolicy, wire.Auth)
	if err := decoded.Validate(); err != nil {
		return err
	}
	*p = decoded
	return nil
}

type SessionLifecycleResultKind string

const (
	SessionLifecycleResultStop          SessionLifecycleResultKind = "stop"
	SessionLifecycleResultSelectSession SessionLifecycleResultKind = "select_session"
	SessionLifecycleResultLaunch        SessionLifecycleResultKind = "launch"
)

type SessionLifecycleResult struct {
	kind        SessionLifecycleResultKind
	auth        *SessionAuthPreparation
	intent      *SessionLaunchIntent
	preparation *SessionLaunchPreparation
}

func StopSessionLifecycleResult() SessionLifecycleResult {
	return SessionLifecycleResult{kind: SessionLifecycleResultStop}
}

func SelectSessionLifecycleResult(auth SessionAuthPreparation) SessionLifecycleResult {
	return SessionLifecycleResult{kind: SessionLifecycleResultSelectSession, auth: &auth}
}

func LaunchSessionLifecycleResult(intent SessionLaunchIntent, preparation SessionLaunchPreparation) SessionLifecycleResult {
	return SessionLifecycleResult{kind: SessionLifecycleResultLaunch, intent: &intent, preparation: &preparation}
}

func (r SessionLifecycleResult) IsZero() bool {
	return r.kind == "" && r.auth == nil && r.intent == nil && r.preparation == nil
}

func (r SessionLifecycleResult) Kind() SessionLifecycleResultKind {
	return r.kind
}

func (r SessionLifecycleResult) AuthPreparation() (SessionAuthPreparation, bool) {
	if r.auth == nil {
		return "", false
	}
	return *r.auth, true
}

func (r SessionLifecycleResult) LaunchIntent() (SessionLaunchIntent, bool) {
	if r.intent == nil {
		return SessionLaunchIntent{}, false
	}
	return *r.intent, true
}

func (r SessionLifecycleResult) LaunchPreparation() (SessionLaunchPreparation, bool) {
	if r.preparation == nil {
		return SessionLaunchPreparation{}, false
	}
	return *r.preparation, true
}

func (r SessionLifecycleResult) Equal(other SessionLifecycleResult) bool {
	if r.kind != other.kind {
		return false
	}
	leftAuth, leftHasAuth := r.AuthPreparation()
	rightAuth, rightHasAuth := other.AuthPreparation()
	if leftHasAuth != rightHasAuth || (leftHasAuth && leftAuth != rightAuth) {
		return false
	}
	leftIntent, leftHasIntent := r.LaunchIntent()
	rightIntent, rightHasIntent := other.LaunchIntent()
	if leftHasIntent != rightHasIntent || (leftHasIntent && !leftIntent.Equal(rightIntent)) {
		return false
	}
	leftPreparation, leftHasPreparation := r.LaunchPreparation()
	rightPreparation, rightHasPreparation := other.LaunchPreparation()
	return leftHasPreparation == rightHasPreparation &&
		(!leftHasPreparation || leftPreparation.Equal(rightPreparation))
}

func (r SessionLifecycleResult) Validate() error {
	switch r.kind {
	case SessionLifecycleResultStop:
		if r.auth != nil || r.intent != nil || r.preparation != nil {
			return errors.New("stop lifecycle result cannot contain a payload")
		}
	case SessionLifecycleResultSelectSession:
		if r.auth == nil {
			return errors.New("select_session lifecycle result requires auth preparation")
		}
		if err := r.auth.Validate(); err != nil {
			return err
		}
		if r.intent != nil || r.preparation != nil {
			return errors.New("select_session lifecycle result cannot contain launch payload")
		}
	case SessionLifecycleResultLaunch:
		if r.auth != nil {
			return errors.New("launch lifecycle result cannot contain select-session auth")
		}
		if r.intent == nil {
			return errors.New("launch lifecycle result requires intent")
		}
		if err := r.intent.Validate(); err != nil {
			return err
		}
		if r.preparation == nil {
			return errors.New("launch lifecycle result requires preparation")
		}
		if err := r.preparation.Validate(); err != nil {
			return err
		}
	default:
		return errors.New("session lifecycle result kind is invalid")
	}
	return nil
}

func (r SessionLifecycleResult) MarshalJSON() ([]byte, error) {
	if err := r.Validate(); err != nil {
		return nil, err
	}
	type wire struct {
		Kind        SessionLifecycleResultKind `json:"kind"`
		Auth        *SessionAuthPreparation    `json:"auth,omitempty"`
		Intent      *SessionLaunchIntent       `json:"intent,omitempty"`
		Preparation *SessionLaunchPreparation  `json:"preparation,omitempty"`
	}
	return json.Marshal(wire{
		Kind:        r.kind,
		Auth:        r.auth,
		Intent:      r.intent,
		Preparation: r.preparation,
	})
}

func (r *SessionLifecycleResult) UnmarshalJSON(data []byte) error {
	var wire struct {
		Kind        SessionLifecycleResultKind `json:"kind"`
		Auth        *SessionAuthPreparation    `json:"auth"`
		Intent      *SessionLaunchIntent       `json:"intent"`
		Preparation *SessionLaunchPreparation  `json:"preparation"`
	}
	if err := decodeStrictJSON(data, &wire); err != nil {
		return err
	}
	decoded := SessionLifecycleResult{
		kind:        wire.Kind,
		auth:        wire.Auth,
		intent:      wire.Intent,
		preparation: wire.Preparation,
	}
	if err := decoded.Validate(); err != nil {
		return err
	}
	*r = decoded
	return nil
}
