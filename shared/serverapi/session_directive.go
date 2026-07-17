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

type SessionNavigationBinding struct {
	ProjectID   string `json:"project_id"`
	WorkspaceID string `json:"workspace_id"`
}

func (b SessionNavigationBinding) Validate() error {
	if normalized := strings.TrimSpace(b.ProjectID); normalized == "" {
		return errors.New("session navigation project_id is required")
	} else if normalized != b.ProjectID {
		return errors.New("session navigation project_id must not have leading or trailing whitespace")
	}
	if normalized := strings.TrimSpace(b.WorkspaceID); normalized == "" {
		return errors.New("session navigation workspace_id is required")
	} else if normalized != b.WorkspaceID {
		return errors.New("session navigation workspace_id must not have leading or trailing whitespace")
	}
	return nil
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

type SessionDraftDispositionKind string

const (
	SessionDraftDispositionRestoreStoredDraft  SessionDraftDispositionKind = "restore_stored_draft"
	SessionDraftDispositionOverrideStoredDraft SessionDraftDispositionKind = "override_stored_draft"
)

type SessionDraftDisposition struct {
	kind         SessionDraftDispositionKind
	overrideText *string
}

func RestoreStoredDraftSessionDraftDisposition() SessionDraftDisposition {
	return SessionDraftDisposition{kind: SessionDraftDispositionRestoreStoredDraft}
}

func OverrideStoredDraftSessionDraftDisposition(text string) SessionDraftDisposition {
	return SessionDraftDisposition{kind: SessionDraftDispositionOverrideStoredDraft, overrideText: &text}
}

func (p SessionDraftDisposition) Kind() SessionDraftDispositionKind {
	return p.kind
}

func (p SessionDraftDisposition) OverrideText() (string, bool) {
	if p.overrideText == nil {
		return "", false
	}
	return *p.overrideText, true
}

func (p SessionDraftDisposition) Validate() error {
	switch p.kind {
	case SessionDraftDispositionRestoreStoredDraft:
		if p.overrideText != nil {
			return errors.New("restore_stored_draft disposition cannot contain text")
		}
	case SessionDraftDispositionOverrideStoredDraft:
		if p.overrideText == nil {
			return errors.New("override_stored_draft disposition requires text")
		}
	default:
		return errors.New("session draft disposition kind is invalid")
	}
	return nil
}

func (p SessionDraftDisposition) MarshalJSON() ([]byte, error) {
	if err := p.Validate(); err != nil {
		return nil, err
	}
	type wire struct {
		Kind SessionDraftDispositionKind `json:"kind"`
		Text *string                     `json:"text,omitempty"`
	}
	return json.Marshal(wire{Kind: p.kind, Text: p.overrideText})
}

func (p *SessionDraftDisposition) UnmarshalJSON(data []byte) error {
	var wire struct {
		Kind SessionDraftDispositionKind `json:"kind"`
		Text *string                     `json:"text"`
	}
	if err := decodeStrictJSON(data, &wire); err != nil {
		return err
	}
	decoded := SessionDraftDisposition{kind: wire.Kind, overrideText: wire.Text}
	if err := decoded.Validate(); err != nil {
		return err
	}
	*p = decoded
	return nil
}

type SessionLaunchPreparation struct {
	initialPrompt     *SessionInitialPromptMetadata
	draftDisposition  SessionDraftDisposition
	auth              SessionAuthPreparation
	navigationBinding *SessionNavigationBinding
}

func NewSessionLaunchPreparation(initialPrompt *SessionInitialPromptMetadata, draftDisposition SessionDraftDisposition, auth SessionAuthPreparation) SessionLaunchPreparation {
	preparation := SessionLaunchPreparation{draftDisposition: draftDisposition, auth: auth}
	if initialPrompt != nil {
		copied := *initialPrompt
		preparation.initialPrompt = &copied
	}
	return preparation
}

func NewSessionNavigationLaunchPreparation(initialPrompt *SessionInitialPromptMetadata, draftDisposition SessionDraftDisposition, auth SessionAuthPreparation, binding SessionNavigationBinding) SessionLaunchPreparation {
	preparation := NewSessionLaunchPreparation(initialPrompt, draftDisposition, auth)
	copied := binding
	preparation.navigationBinding = &copied
	return preparation
}

func (p SessionLaunchPreparation) InitialPrompt() (SessionInitialPromptMetadata, bool) {
	if p.initialPrompt == nil {
		return SessionInitialPromptMetadata{}, false
	}
	return *p.initialPrompt, true
}

func (p SessionLaunchPreparation) DraftDisposition() SessionDraftDisposition {
	return p.draftDisposition
}

func (p SessionLaunchPreparation) AuthPreparation() SessionAuthPreparation {
	return p.auth
}

func (p SessionLaunchPreparation) NavigationBinding() (SessionNavigationBinding, bool) {
	if p.navigationBinding == nil {
		return SessionNavigationBinding{}, false
	}
	return *p.navigationBinding, true
}

func (p SessionLaunchPreparation) Validate() error {
	if p.initialPrompt != nil {
		if err := p.initialPrompt.Validate(); err != nil {
			return err
		}
	}
	if err := p.draftDisposition.Validate(); err != nil {
		return err
	}
	if err := p.auth.Validate(); err != nil {
		return err
	}
	if p.navigationBinding != nil {
		return p.navigationBinding.Validate()
	}
	return nil
}

func (p SessionLaunchPreparation) MarshalJSON() ([]byte, error) {
	if err := p.Validate(); err != nil {
		return nil, err
	}
	type wire struct {
		InitialPrompt     *SessionInitialPromptMetadata `json:"initial_prompt,omitempty"`
		InputPolicy       SessionDraftDisposition       `json:"input_policy"`
		Auth              SessionAuthPreparation        `json:"auth"`
		NavigationBinding *SessionNavigationBinding     `json:"navigation_binding,omitempty"`
	}
	return json.Marshal(wire{
		InitialPrompt:     p.initialPrompt,
		InputPolicy:       p.draftDisposition,
		Auth:              p.auth,
		NavigationBinding: p.navigationBinding,
	})
}

func (p *SessionLaunchPreparation) UnmarshalJSON(data []byte) error {
	var wire struct {
		InitialPrompt     *SessionInitialPromptMetadata `json:"initial_prompt"`
		InputPolicy       SessionDraftDisposition       `json:"input_policy"`
		Auth              SessionAuthPreparation        `json:"auth"`
		NavigationBinding *SessionNavigationBinding     `json:"navigation_binding"`
	}
	if err := decodeStrictJSON(data, &wire); err != nil {
		return err
	}
	decoded := NewSessionLaunchPreparation(wire.InitialPrompt, wire.InputPolicy, wire.Auth)
	if wire.NavigationBinding != nil {
		copied := *wire.NavigationBinding
		decoded.navigationBinding = &copied
	}
	if err := decoded.Validate(); err != nil {
		return err
	}
	*p = decoded
	return nil
}

type SessionDirectiveKind string

const (
	SessionDirectiveStop          SessionDirectiveKind = "stop"
	SessionDirectiveSelectSession SessionDirectiveKind = "select_session"
	SessionDirectiveLaunch        SessionDirectiveKind = "launch"
)

type SessionDirective struct {
	kind        SessionDirectiveKind
	auth        *SessionAuthPreparation
	intent      *SessionLaunchIntent
	preparation *SessionLaunchPreparation
}

func StopSessionDirective() SessionDirective {
	return SessionDirective{kind: SessionDirectiveStop}
}

func SelectSessionDirective(auth SessionAuthPreparation) SessionDirective {
	return SessionDirective{kind: SessionDirectiveSelectSession, auth: &auth}
}

func LaunchSessionDirective(intent SessionLaunchIntent, preparation SessionLaunchPreparation) SessionDirective {
	return SessionDirective{kind: SessionDirectiveLaunch, intent: &intent, preparation: &preparation}
}

func (r SessionDirective) Kind() SessionDirectiveKind {
	return r.kind
}

func (r SessionDirective) AuthPreparation() (SessionAuthPreparation, bool) {
	if r.auth == nil {
		return "", false
	}
	return *r.auth, true
}

func (r SessionDirective) LaunchIntent() (SessionLaunchIntent, bool) {
	if r.intent == nil {
		return SessionLaunchIntent{}, false
	}
	return *r.intent, true
}

func (r SessionDirective) LaunchPreparation() (SessionLaunchPreparation, bool) {
	if r.preparation == nil {
		return SessionLaunchPreparation{}, false
	}
	return *r.preparation, true
}

func (r SessionDirective) Validate() error {
	switch r.kind {
	case SessionDirectiveStop:
		if r.auth != nil || r.intent != nil || r.preparation != nil {
			return errors.New("stop session directive cannot contain a payload")
		}
	case SessionDirectiveSelectSession:
		if r.auth == nil {
			return errors.New("select_session directive requires auth preparation")
		}
		if err := r.auth.Validate(); err != nil {
			return err
		}
		if r.intent != nil || r.preparation != nil {
			return errors.New("select_session directive cannot contain launch payload")
		}
	case SessionDirectiveLaunch:
		if r.auth != nil {
			return errors.New("launch directive cannot contain select-session auth")
		}
		if r.intent == nil {
			return errors.New("launch directive requires intent")
		}
		if err := r.intent.Validate(); err != nil {
			return err
		}
		if r.preparation == nil {
			return errors.New("launch directive requires preparation")
		}
		if err := r.preparation.Validate(); err != nil {
			return err
		}
	default:
		return errors.New("session directive kind is invalid")
	}
	return nil
}

func (r SessionDirective) MarshalJSON() ([]byte, error) {
	if err := r.Validate(); err != nil {
		return nil, err
	}
	type wire struct {
		Kind        SessionDirectiveKind      `json:"kind"`
		Auth        *SessionAuthPreparation   `json:"auth,omitempty"`
		Intent      *SessionLaunchIntent      `json:"intent,omitempty"`
		Preparation *SessionLaunchPreparation `json:"preparation,omitempty"`
	}
	return json.Marshal(wire{
		Kind:        r.kind,
		Auth:        r.auth,
		Intent:      r.intent,
		Preparation: r.preparation,
	})
}

func (r *SessionDirective) UnmarshalJSON(data []byte) error {
	var wire struct {
		Kind        SessionDirectiveKind      `json:"kind"`
		Auth        *SessionAuthPreparation   `json:"auth"`
		Intent      *SessionLaunchIntent      `json:"intent"`
		Preparation *SessionLaunchPreparation `json:"preparation"`
	}
	if err := decodeStrictJSON(data, &wire); err != nil {
		return err
	}
	decoded := SessionDirective{
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
