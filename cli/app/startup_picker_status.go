package app

import (
	"fmt"
	"strings"

	"core/shared/sessioncontract"
	sharedtheme "core/shared/theme"

	"github.com/charmbracelet/lipgloss"
)

type startupPickerStatusFailure struct {
	Operation  startupPickerStatusOperation
	Generation uint64
	Kind       sessionPickerFailureKind
	Diagnostic error
}

type startupPickerStatusOperation interface {
	isStartupPickerStatusOperation()
}

type startupPickerSessionOperation struct {
	tab  sessioncontract.SessionCategory
	kind sessionPickerOperationKind
}

func (startupPickerSessionOperation) isStartupPickerStatusOperation() {}

type startupPickerWorkspaceOperationKind uint8

const (
	startupPickerWorkspaceOperationFirstPage startupPickerWorkspaceOperationKind = iota + 1
	startupPickerWorkspaceOperationPreviousEdge
	startupPickerWorkspaceOperationNextEdge
)

type startupPickerWorkspaceOperation struct {
	kind startupPickerWorkspaceOperationKind
}

func (startupPickerWorkspaceOperation) isStartupPickerStatusOperation() {}

type sessionPickerOperationKind uint8

const (
	sessionPickerOperationBodyPage sessionPickerOperationKind = iota + 1
	sessionPickerOperationDirectionalPage
)

type sessionPickerFailureKind string

const (
	sessionPickerFailurePageRequest  sessionPickerFailureKind = "page_request"
	sessionPickerFailurePageContract sessionPickerFailureKind = "page_contract"
)

type startupPickerStatusKey struct {
	operation startupPickerStatusOperation
}

type startupPickerStatusRecord struct {
	failure  startupPickerStatusFailure
	sequence uint64
}

type startupPickerStatusModel struct {
	notice    startupPickerNotice
	failures  map[startupPickerStatusKey]startupPickerStatusRecord
	sequence  uint64
	activeTab *sessioncontract.SessionCategory
}

func newStartupPickerStatusModel() *startupPickerStatusModel {
	return &startupPickerStatusModel{
		failures: make(map[startupPickerStatusKey]startupPickerStatusRecord),
	}
}

func (m *startupPickerStatusModel) Record(failure startupPickerStatusFailure) {
	if m == nil {
		return
	}
	if failure.Diagnostic == nil {
		panic("startup picker failure requires a diagnostic")
	}
	if failure.Operation == nil {
		panic("startup picker failure requires an operation")
	}
	switch operation := failure.Operation.(type) {
	case startupPickerSessionOperation:
		if _, err := sessioncontract.ParseSessionCategory(string(operation.tab)); err != nil {
			panic(fmt.Sprintf("invalid session picker operation tab %q", operation.tab))
		}
		switch operation.kind {
		case sessionPickerOperationBodyPage, sessionPickerOperationDirectionalPage:
		default:
			panic(fmt.Sprintf("unknown session picker operation kind %d", operation.kind))
		}
	case startupPickerWorkspaceOperation:
		switch operation.kind {
		case startupPickerWorkspaceOperationFirstPage,
			startupPickerWorkspaceOperationPreviousEdge,
			startupPickerWorkspaceOperationNextEdge:
		default:
			panic(fmt.Sprintf("unknown Workspace picker operation kind %d", operation.kind))
		}
	default:
		panic(fmt.Sprintf("unknown startup picker operation type %T", failure.Operation))
	}
	switch failure.Kind {
	case sessionPickerFailurePageRequest,
		sessionPickerFailurePageContract:
	default:
		panic(fmt.Sprintf("unknown session picker failure kind %q", failure.Kind))
	}
	if m.failures == nil {
		m.failures = make(map[startupPickerStatusKey]startupPickerStatusRecord)
	}
	m.sequence++
	m.failures[startupPickerStatusKey{operation: failure.Operation}] = startupPickerStatusRecord{
		failure:  failure,
		sequence: m.sequence,
	}
}

func (m *startupPickerStatusModel) Clear(tab sessioncontract.SessionCategory, operation sessionPickerOperationKind, generation uint64) {
	if m == nil {
		return
	}
	key := startupPickerStatusKey{operation: startupPickerSessionOperation{tab: tab, kind: operation}}
	record, ok := m.failures[key]
	if ok && record.failure.Generation <= generation {
		delete(m.failures, key)
	}
}

func (m *startupPickerStatusModel) ClearWorkspace(operation startupPickerWorkspaceOperationKind, generation uint64) {
	if m == nil {
		return
	}
	key := startupPickerStatusKey{operation: startupPickerWorkspaceOperation{kind: operation}}
	record, ok := m.failures[key]
	if ok && record.failure.Generation <= generation {
		delete(m.failures, key)
	}
}

func (m *startupPickerStatusModel) failure(tab sessioncontract.SessionCategory, operation sessionPickerOperationKind) (startupPickerStatusFailure, bool) {
	if m == nil {
		return startupPickerStatusFailure{}, false
	}
	record, ok := m.failures[startupPickerStatusKey{operation: startupPickerSessionOperation{tab: tab, kind: operation}}]
	if !ok {
		return startupPickerStatusFailure{}, false
	}
	return record.failure, true
}

type startupPickerStatusProjection struct {
	Notice  startupPickerNotice
	Failure *startupPickerStatusFailure
}

type startupPickerStatusRenderKind uint8

const (
	startupPickerStatusRenderNotice startupPickerStatusRenderKind = iota + 1
	startupPickerStatusRenderFailure
)

type startupPickerStatusRenderProjection struct {
	Kind    startupPickerStatusRenderKind
	Text    string
	IsError bool
}

func projectStartupPickerStatus(model *startupPickerStatusModel) startupPickerStatusProjection {
	if model == nil {
		return startupPickerStatusProjection{}
	}
	projection := startupPickerStatusProjection{Notice: model.notice}
	var newest startupPickerStatusRecord
	found := false
	for key, record := range model.failures {
		if model.activeTab != nil {
			operation, ok := key.operation.(startupPickerSessionOperation)
			if ok && operation.tab != *model.activeTab {
				continue
			}
		}
		if !found || record.sequence > newest.sequence {
			newest, found = record, true
		}
	}
	if !found {
		for _, record := range model.failures {
			if !found || record.sequence > newest.sequence {
				newest, found = record, true
			}
		}
	}
	if found {
		failure := newest.failure
		projection.Failure = &failure
	}
	return projection
}

type startupPickerStatusSurface struct {
	model *startupPickerStatusModel
}

func newSessionPickerStatusSurface(model *startupPickerStatusModel) startupPickerStatusSurface {
	return startupPickerStatusSurface{model: model}
}

func (s startupPickerStatusSurface) RenderStatus(width int) string {
	return renderStartupPickerStatus(projectStartupPickerStatus(s.model), width)
}

func renderStartupPickerStatus(projection startupPickerStatusProjection, width int) string {
	renderProjection := projectStartupPickerStatusRender(projection)
	if renderProjection == nil {
		return ""
	}
	text := strings.TrimSpace(renderProjection.Text)
	isError := renderProjection.IsError
	if text == "" {
		return ""
	}
	if width < 1 {
		width = 1
	}
	palette := uiPalette("dark")
	style := lipgloss.NewStyle().Foreground(palette.foreground)
	if isError {
		style = lipgloss.NewStyle().Foreground(sharedtheme.DefaultPalette().Status.Error.Adaptive()).Bold(true)
	}
	return style.Render(truncateQueuedMessageLine(text, width))
}

func projectStartupPickerStatusRender(projection startupPickerStatusProjection) *startupPickerStatusRenderProjection {
	if projection.Failure != nil {
		text := sessionPickerFailureOperatorText(projection.Failure.Kind)
		if _, ok := projection.Failure.Operation.(startupPickerWorkspaceOperation); ok &&
			projection.Failure.Diagnostic != nil {
			text = projection.Failure.Diagnostic.Error()
		}
		return &startupPickerStatusRenderProjection{
			Kind:    startupPickerStatusRenderFailure,
			Text:    text,
			IsError: true,
		}
	}
	if strings.TrimSpace(projection.Notice.Text) == "" {
		return nil
	}
	return &startupPickerStatusRenderProjection{
		Kind:    startupPickerStatusRenderNotice,
		Text:    projection.Notice.Text,
		IsError: projection.Notice.Kind == startupPickerNoticeError,
	}
}

func renderStartupPickerNotice(notice startupPickerNotice, width int) string {
	return renderStartupPickerStatus(startupPickerStatusProjection{Notice: notice}, width)
}

func sessionPickerFailureOperatorText(kind sessionPickerFailureKind) string {
	switch kind {
	case sessionPickerFailurePageRequest, sessionPickerFailurePageContract:
		return "Sessions are unavailable. Try again."
	default:
		panic(fmt.Sprintf("unknown session picker failure kind %q", kind))
	}
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
		Operation:  startupPickerSessionOperation{tab: tab.category, kind: operation},
		Generation: generation, Kind: kind, Diagnostic: diagnostic,
	})
}

func (m *sessionPickerModel) clearPickerFailureForTab(tab *sessionPickerTab, kind sessionPickerOperationKind, generation uint64) {
	if m.startupStatus != nil {
		m.startupStatus.Clear(tab.category, kind, generation)
	}
}

type sessionPickerTabsProjectionInput struct {
	Width     int
	ActiveTab sessioncontract.SessionCategory
	Geometry  terminalGeometry
	Theme     string
}

type sessionPickerTabsProjection struct {
	Rows []string
}

func projectSessionPickerTabs(input sessionPickerTabsProjectionInput) sessionPickerTabsProjection {
	width := input.Width
	if width < 1 {
		width = 1
	}
	selected := 0
	if input.ActiveTab == sessioncontract.SessionCategorySubagent {
		selected = 1
	}
	options := []uiChoiceOption{{Label: "Sessions"}, {Label: "Subagents"}}
	size := input.Geometry.Size()
	stacked := width >= 40 && width < 48 && (size == nil || size.height >= 10)
	projection := sessionPickerTabsProjection{}
	if stacked {
		projection.Rows = []string{
			renderUIChoiceGroupLine(width, input.Theme, uiChoiceGroupKindButton, options[:1], 0),
			renderUIChoiceGroupLine(width, input.Theme, uiChoiceGroupKindButton, options[1:], selected-1),
		}
	} else {
		projection.Rows = []string{renderUIChoiceGroupLine(width, input.Theme, uiChoiceGroupKindButton, options, selected)}
	}
	return projection
}
