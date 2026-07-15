package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"core/shared/runtimeids"
	"core/shared/serverapi"
	"core/shared/sessioncontract"

	tea "github.com/charmbracelet/bubbletea"
)

type sessionPickerOperationKind string

const (
	sessionPickerOperationBodyPage        sessionPickerOperationKind = "body_page"
	sessionPickerOperationDirectionalPage sessionPickerOperationKind = "directional_page"
	sessionPickerOperationSelectedDetail  sessionPickerOperationKind = "selected_detail"
)

type sessionPickerSelectedDetail interface {
	isSessionPickerSelectedDetail()
}

type sessionPickerSelectedDetailIdentity struct {
	sessionID  runtimeids.SessionID
	generation uint64
}

type sessionPickerSelectedDetailLoadingState struct {
	identity sessionPickerSelectedDetailIdentity
}

type sessionPickerSelectedDetailReadyState struct {
	identity  sessionPickerSelectedDetailIdentity
	workspace serverapi.SessionExecutionWorkspaceField
	branch    serverapi.SessionExecutionBranchField
	auth      serverapi.SessionExecutionAuthField
	model     serverapi.SessionExecutionModelField
}

type sessionPickerSelectedDetailFailedState struct {
	identity sessionPickerSelectedDetailIdentity
	err      error
}

type sessionPickerDetailFieldProjection struct {
	text   string
	failed bool
}

func (sessionPickerSelectedDetailLoadingState) isSessionPickerSelectedDetail() {}
func (sessionPickerSelectedDetailReadyState) isSessionPickerSelectedDetail()   {}
func (sessionPickerSelectedDetailFailedState) isSessionPickerSelectedDetail()  {}

func newSessionPickerSelectedDetailIdentity(
	sessionID runtimeids.SessionID,
	generation uint64,
) sessionPickerSelectedDetailIdentity {
	if sessionID.IsZero() || generation == 0 {
		panic("selected session detail identity requires a session ID and generation")
	}
	return sessionPickerSelectedDetailIdentity{sessionID: sessionID, generation: generation}
}

type sessionPickerDetailRequest struct {
	category   sessioncontract.SessionCategory
	sessionID  runtimeids.SessionID
	generation uint64
	ctx        context.Context
	cancel     context.CancelFunc
}

func (r *sessionPickerDetailRequest) IsCanceled() bool {
	if r == nil {
		return true
	}
	select {
	case <-r.ctx.Done():
		return true
	default:
		return false
	}
}

type sessionPickerSelectedDetailLoadedMsg struct {
	Category   sessioncontract.SessionCategory
	SessionID  runtimeids.SessionID
	Generation uint64
	Response   serverapi.SessionExecutionEnvironmentResponse
	Err        error
}

func validateSessionPickerPage(
	response serverapi.SessionPageResponse,
	expectedProjectID string,
	expectedCategory sessioncontract.SessionCategory,
) error {
	if err := response.Validate(); err != nil {
		return fmt.Errorf("session picker page is invalid: %w", err)
	}
	if response.ProjectID != expectedProjectID {
		return fmt.Errorf(
			"session picker page project %q does not match requested project %q",
			response.ProjectID,
			expectedProjectID,
		)
	}
	if response.Category != expectedCategory {
		return fmt.Errorf(
			"session picker page category %q does not match requested category %q",
			response.Category,
			expectedCategory,
		)
	}
	return nil
}

func (m *sessionPickerModel) startSelectedDetailForTab(tab *sessionPickerTab) tea.Cmd {
	selected, ok := tab.selected.(sessionPickerSessionSelection)
	if !ok {
		m.discardSelectedDetailForTab(tab)
		tab.selectedDetail = nil
		return nil
	}
	return m.startSelectedDetailForTabWithID(tab, selected.sessionID)
}

func (m *sessionPickerModel) startSelectedDetailForTabWithID(tab *sessionPickerTab, id runtimeids.SessionID) tea.Cmd {
	if m.executionEnvironmentClient == nil || id.IsZero() {
		return nil
	}
	if tab.detailRequest != nil && tab.detailRequest.sessionID == id && !tab.detailRequest.IsCanceled() {
		return m.loadSelectedDetailCmd(tab.detailRequest)
	}
	m.discardSelectedDetailForTab(tab)
	tab.generation++
	ctx, cancel := context.WithCancel(m.requestContext)
	request := &sessionPickerDetailRequest{
		category: tab.category, sessionID: id, generation: tab.generation, ctx: ctx, cancel: cancel,
	}
	tab.detailRequest = request
	tab.selectedDetail = sessionPickerSelectedDetailLoadingState{
		identity: newSessionPickerSelectedDetailIdentity(id, request.generation),
	}
	return m.loadSelectedDetailCmd(request)
}

func (m *sessionPickerModel) loadSelectedDetailCmd(request *sessionPickerDetailRequest) tea.Cmd {
	if request == nil || m.executionEnvironmentClient == nil {
		return nil
	}
	client := m.executionEnvironmentClient
	return func() tea.Msg {
		response, err := client.GetSessionExecutionEnvironment(request.ctx, serverapi.SessionExecutionEnvironmentRequest{
			SessionID: request.sessionID,
		})
		return sessionPickerSelectedDetailLoadedMsg{
			Category: request.category, SessionID: request.sessionID, Generation: request.generation,
			Response: response, Err: err,
		}
	}
}

func (m *sessionPickerModel) applySelectedDetailLoaded(message sessionPickerSelectedDetailLoadedMsg) tea.Cmd {
	tab := m.tab(message.Category)
	request := tab.detailRequest
	if request == nil ||
		request.sessionID != message.SessionID ||
		request.generation != message.Generation ||
		request.IsCanceled() {
		return nil
	}
	tab.detailRequest = nil
	if message.Err != nil {
		tab.selectedDetail = sessionPickerSelectedDetailFailedState{
			identity: newSessionPickerSelectedDetailIdentity(message.SessionID, message.Generation),
			err:      message.Err,
		}
		m.recordPickerFailureForTab(
			tab,
			sessionPickerOperationSelectedDetail,
			message.Generation,
			sessionPickerFailureDetailRequest,
			message.Err,
		)
		return nil
	}
	if err := message.Response.Validate(); err != nil {
		return m.failSelectedDetail(
			tab,
			message,
			sessionPickerFailureDetailContract,
			fmt.Errorf("selected session environment is invalid: %w", err),
		)
	}
	if message.Response.Environment.SessionID != message.SessionID {
		return m.failSelectedDetail(
			tab,
			message,
			sessionPickerFailureDetailContract,
			fmt.Errorf(
				"selected session environment identity %q does not match requested session %q",
				message.Response.Environment.SessionID.String(),
				message.SessionID.String(),
			),
		)
	}
	environment := message.Response.Environment
	detail := sessionPickerSelectedDetailReadyState{
		identity:  newSessionPickerSelectedDetailIdentity(message.SessionID, message.Generation),
		workspace: environment.Workspace, branch: environment.Branch,
		auth: environment.Auth, model: environment.Model,
	}
	tab.selectedDetail = detail
	if environment.Workspace.Kind() == serverapi.SessionExecutionFieldFailed {
		err, _ := environment.Workspace.Failure()
		m.recordPickerFailureForTab(tab, sessionPickerOperationSelectedDetail, message.Generation, sessionPickerFailureDetailField, errors.New(err.Message))
	}
	if environment.Branch.Kind() == serverapi.SessionExecutionFieldFailed {
		err, _ := environment.Branch.Failure()
		m.recordPickerFailureForTab(tab, sessionPickerOperationSelectedDetail, message.Generation, sessionPickerFailureDetailField, errors.New(err.Message))
	}
	if environment.Auth.Kind() == serverapi.SessionExecutionFieldFailed {
		err, _ := environment.Auth.Failure()
		m.recordPickerFailureForTab(tab, sessionPickerOperationSelectedDetail, message.Generation, sessionPickerFailureDetailField, errors.New(err.Message))
	}
	if environment.Model.Kind() == serverapi.SessionExecutionFieldFailed {
		err, _ := environment.Model.Failure()
		m.recordPickerFailureForTab(tab, sessionPickerOperationSelectedDetail, message.Generation, sessionPickerFailureDetailField, errors.New(err.Message))
	}
	if !tabHasSelectedDetailFailure(tab) {
		m.clearPickerFailureForTab(tab, sessionPickerOperationSelectedDetail, message.Generation)
	}
	return nil
}

func (m *sessionPickerModel) failSelectedDetail(
	tab *sessionPickerTab,
	message sessionPickerSelectedDetailLoadedMsg,
	kind sessionPickerFailureKind,
	err error,
) tea.Cmd {
	tab.selectedDetail = sessionPickerSelectedDetailFailedState{
		identity: newSessionPickerSelectedDetailIdentity(message.SessionID, message.Generation),
		err:      err,
	}
	m.recordPickerFailureForTab(tab, sessionPickerOperationSelectedDetail, message.Generation, kind, err)
	return nil
}

func tabHasSelectedDetailFailure(tab *sessionPickerTab) bool {
	detail, ok := tab.selectedDetail.(sessionPickerSelectedDetailReadyState)
	if !ok {
		return false
	}
	return detail.workspace.Kind() == serverapi.SessionExecutionFieldFailed ||
		detail.branch.Kind() == serverapi.SessionExecutionFieldFailed ||
		detail.auth.Kind() == serverapi.SessionExecutionFieldFailed ||
		detail.model.Kind() == serverapi.SessionExecutionFieldFailed
}

func (m *sessionPickerModel) cancelSelectedDetailForTab(tab *sessionPickerTab) {
	if tab.detailRequest != nil {
		tab.detailRequest.cancel()
		tab.detailRequest = nil
	}
}

func (m *sessionPickerModel) discardSelectedDetailForTab(tab *sessionPickerTab) {
	m.cancelSelectedDetailForTab(tab)
	m.clearPickerFailureForTab(tab, sessionPickerOperationSelectedDetail, tab.generation)
}

func (m *sessionPickerModel) cancelSelectedDetailRequests() {
	m.cancelSelectedDetailForTab(&m.main)
	m.cancelSelectedDetailForTab(&m.subagents)
}

func (m *sessionPickerModel) recordPickerFailureForTab(
	tab *sessionPickerTab,
	operation sessionPickerOperationKind,
	generation uint64,
	kind sessionPickerFailureKind,
	diagnostic error,
) {
	if m.startupStatus == nil {
		m.startupStatus = newStartupPickerStatusModel()
	}
	m.startupStatus.Record(startupPickerStatusFailure{
		Tab: tab.category, Operation: operation, Generation: generation, Kind: kind, Diagnostic: diagnostic,
		ActiveEligible: operation != sessionPickerOperationSelectedDetail || selectedSessionIsActive(tab),
	})
}

func selectedSessionIsActive(tab *sessionPickerTab) bool {
	_, ok := tab.selected.(sessionPickerSessionSelection)
	return ok
}

func (m *sessionPickerModel) clearPickerFailureForTab(tab *sessionPickerTab, kind sessionPickerOperationKind, generation uint64) {
	if m.startupStatus != nil {
		m.startupStatus.Clear(tab.category, kind, generation)
	}
}

func (m *sessionPickerModel) selectedDetailLineCount(tab *sessionPickerTab) int {
	if !selectedSessionIsActive(tab) || tab.selectedDetail == nil {
		return 0
	}
	return 4
}

func (m *sessionPickerModel) renderSelectedDetail(tab *sessionPickerTab) string {
	if !selectedSessionIsActive(tab) || tab.selectedDetail == nil {
		return ""
	}
	workspace := sessionPickerDetailFieldProjection{text: "—"}
	branch := sessionPickerDetailFieldProjection{text: "—"}
	auth := sessionPickerDetailFieldProjection{text: "—"}
	model := sessionPickerDetailFieldProjection{text: "—"}
	switch detail := tab.selectedDetail.(type) {
	case sessionPickerSelectedDetailLoadingState:
		workspace.text, branch.text, auth.text, model.text = "…", "…", "…", "…"
	case sessionPickerSelectedDetailReadyState:
		workspace = renderExecutionWorkspace(detail.workspace)
		branch = renderExecutionBranch(detail.branch)
		auth = renderExecutionAuth(detail.auth)
		model = renderExecutionModel(detail.model)
	case sessionPickerSelectedDetailFailedState:
	default:
		panic(fmt.Sprintf("unknown selected session detail state %T", tab.selectedDetail))
	}
	lines := []string{
		m.renderSelectedDetailField("Workspace", workspace),
		m.renderSelectedDetailField("Branch", branch),
		m.renderSelectedDetailField("Auth", auth),
		m.renderSelectedDetailField("Model", model),
	}
	return strings.Join(lines, "\n")
}

func (m *sessionPickerModel) renderSelectedDetailField(label string, value sessionPickerDetailFieldProjection) string {
	labelStyle := m.styles.preview
	valueStyle := m.styles.headerText
	if value.failed {
		valueStyle = m.styles.headerWarning
	}
	return labelStyle.Render(fmt.Sprintf("%-9s", label)) + valueStyle.Render(value.text)
}

func renderExecutionWorkspace(field serverapi.SessionExecutionWorkspaceField) sessionPickerDetailFieldProjection {
	if value, ok := field.Value(); ok {
		return sessionPickerDetailFieldProjection{text: value.Path}
	}
	if reason, ok := field.UnavailableReason(); ok {
		switch reason {
		case serverapi.SessionExecutionWorkspaceUnavailableNotConfigured:
			return sessionPickerDetailFieldProjection{text: "not configured"}
		default:
			panic(fmt.Sprintf("unknown session execution workspace unavailable reason %q", reason))
		}
	}
	if _, ok := field.Failure(); ok {
		return sessionPickerDetailFieldProjection{text: "unavailable", failed: true}
	}
	return sessionPickerDetailFieldProjection{text: "—"}
}

func renderExecutionBranch(field serverapi.SessionExecutionBranchField) sessionPickerDetailFieldProjection {
	if value, ok := field.Value(); ok {
		return sessionPickerDetailFieldProjection{text: value.Name}
	}
	if reason, ok := field.UnavailableReason(); ok {
		switch reason {
		case serverapi.SessionExecutionBranchUnavailableDetachedHead:
			return sessionPickerDetailFieldProjection{text: "detached HEAD"}
		case serverapi.SessionExecutionBranchUnavailableNotGitRepository:
			return sessionPickerDetailFieldProjection{text: "not a Git repository"}
		default:
			panic(fmt.Sprintf("unknown session execution branch unavailable reason %q", reason))
		}
	}
	if _, ok := field.Failure(); ok {
		return sessionPickerDetailFieldProjection{text: "unavailable", failed: true}
	}
	return sessionPickerDetailFieldProjection{text: "—"}
}

func renderExecutionAuth(field serverapi.SessionExecutionAuthField) sessionPickerDetailFieldProjection {
	if value, ok := field.Value(); ok {
		switch value.Method {
		case serverapi.SessionExecutionAuthMethodNone:
			return sessionPickerDetailFieldProjection{text: "no auth"}
		case serverapi.SessionExecutionAuthMethodAPIKey:
			return sessionPickerDetailFieldProjection{text: "API key"}
		case serverapi.SessionExecutionAuthMethodOAuth:
			return sessionPickerDetailFieldProjection{text: "OAuth"}
		default:
			panic(fmt.Sprintf("unknown session execution auth method %q", value.Method))
		}
	}
	if reason, ok := field.UnavailableReason(); ok {
		switch reason {
		case serverapi.SessionExecutionAuthUnavailableNotApplicable:
			return sessionPickerDetailFieldProjection{text: "not applicable"}
		default:
			panic(fmt.Sprintf("unknown session execution auth unavailable reason %q", reason))
		}
	}
	if _, ok := field.Failure(); ok {
		return sessionPickerDetailFieldProjection{text: "unavailable", failed: true}
	}
	return sessionPickerDetailFieldProjection{text: "—"}
}

func renderExecutionModel(field serverapi.SessionExecutionModelField) sessionPickerDetailFieldProjection {
	if value, ok := field.Value(); ok {
		return sessionPickerDetailFieldProjection{text: value.Name}
	}
	if reason, ok := field.UnavailableReason(); ok {
		switch reason {
		case serverapi.SessionExecutionModelUnavailableNotConfigured:
			return sessionPickerDetailFieldProjection{text: "not configured"}
		default:
			panic(fmt.Sprintf("unknown session execution model unavailable reason %q", reason))
		}
	}
	if _, ok := field.Failure(); ok {
		return sessionPickerDetailFieldProjection{text: "unavailable", failed: true}
	}
	return sessionPickerDetailFieldProjection{text: "—"}
}

type sessionPickerRelativeAgeBucket string

const (
	sessionPickerRelativeAgeJustNow sessionPickerRelativeAgeBucket = "just_now"
	sessionPickerRelativeAgeMinutes sessionPickerRelativeAgeBucket = "minutes"
	sessionPickerRelativeAgeHours   sessionPickerRelativeAgeBucket = "hours"
	sessionPickerRelativeAgeDays    sessionPickerRelativeAgeBucket = "days"
	sessionPickerRelativeAgeWeeks   sessionPickerRelativeAgeBucket = "weeks"
	sessionPickerRelativeAgeMonths  sessionPickerRelativeAgeBucket = "months"
	sessionPickerRelativeAgeFuture  sessionPickerRelativeAgeBucket = "future"
)

type sessionPickerRelativeAge struct {
	Bucket sessionPickerRelativeAgeBucket
	Amount int
}

func (a sessionPickerRelativeAge) String() string {
	switch a.Bucket {
	case sessionPickerRelativeAgeFuture:
		return "in the future"
	case sessionPickerRelativeAgeJustNow:
		return "just now"
	case sessionPickerRelativeAgeMinutes:
		return fmt.Sprintf("%dm ago", a.Amount)
	case sessionPickerRelativeAgeHours:
		return fmt.Sprintf("%dh ago", a.Amount)
	case sessionPickerRelativeAgeDays:
		return fmt.Sprintf("%dd ago", a.Amount)
	case sessionPickerRelativeAgeWeeks:
		return fmt.Sprintf("%dw ago", a.Amount)
	case sessionPickerRelativeAgeMonths:
		return fmt.Sprintf("%dmo ago", a.Amount)
	default:
		return "—"
	}
}

func relativeSessionAge(updatedAt, now time.Time) sessionPickerRelativeAge {
	age := now.Sub(updatedAt)
	if age < 0 {
		return sessionPickerRelativeAge{Bucket: sessionPickerRelativeAgeFuture}
	}
	switch {
	case age < time.Minute:
		return sessionPickerRelativeAge{Bucket: sessionPickerRelativeAgeJustNow}
	case age < time.Hour:
		return sessionPickerRelativeAge{Bucket: sessionPickerRelativeAgeMinutes, Amount: int(age / time.Minute)}
	case age < 24*time.Hour:
		return sessionPickerRelativeAge{Bucket: sessionPickerRelativeAgeHours, Amount: int(age / time.Hour)}
	case age < 7*24*time.Hour:
		return sessionPickerRelativeAge{Bucket: sessionPickerRelativeAgeDays, Amount: int(age / (24 * time.Hour))}
	case age < 30*24*time.Hour:
		return sessionPickerRelativeAge{Bucket: sessionPickerRelativeAgeWeeks, Amount: int(age / (7 * 24 * time.Hour))}
	default:
		return sessionPickerRelativeAge{Bucket: sessionPickerRelativeAgeMonths, Amount: int(age / (30 * 24 * time.Hour))}
	}
}
