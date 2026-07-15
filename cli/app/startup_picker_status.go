package app

import (
	"fmt"
	"strings"

	"core/shared/sessioncontract"
	sharedtheme "core/shared/theme"

	"github.com/charmbracelet/lipgloss"
)

type startupPickerStatusFailure struct {
	Tab        sessioncontract.SessionCategory
	Operation  sessionPickerOperationKind
	Generation uint64
	Kind       sessionPickerFailureKind
	Diagnostic error
}

type sessionPickerOperationKind string

const (
	sessionPickerOperationBodyPage        sessionPickerOperationKind = "body_page"
	sessionPickerOperationDirectionalPage sessionPickerOperationKind = "directional_page"
)

type sessionPickerFailureKind string

const (
	sessionPickerFailurePageRequest  sessionPickerFailureKind = "page_request"
	sessionPickerFailurePageContract sessionPickerFailureKind = "page_contract"
)

type startupPickerStatusKey struct {
	tab       sessioncontract.SessionCategory
	operation sessionPickerOperationKind
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
	m.failures[startupPickerStatusKey{tab: failure.Tab, operation: failure.Operation}] = startupPickerStatusRecord{
		failure:  failure,
		sequence: m.sequence,
	}
}

func (m *startupPickerStatusModel) Clear(tab sessioncontract.SessionCategory, operation sessionPickerOperationKind, generation uint64) {
	if m == nil {
		return
	}
	key := startupPickerStatusKey{tab: tab, operation: operation}
	record, ok := m.failures[key]
	if ok && record.failure.Generation <= generation {
		delete(m.failures, key)
	}
}

func (m *startupPickerStatusModel) failure(tab sessioncontract.SessionCategory, operation sessionPickerOperationKind) (startupPickerStatusFailure, bool) {
	if m == nil {
		return startupPickerStatusFailure{}, false
	}
	record, ok := m.failures[startupPickerStatusKey{tab: tab, operation: operation}]
	if !ok {
		return startupPickerStatusFailure{}, false
	}
	return record.failure, true
}

type startupPickerStatusProjection struct {
	Notice  startupPickerNotice
	Failure *startupPickerStatusFailure
}

func projectStartupPickerStatus(model *startupPickerStatusModel) startupPickerStatusProjection {
	if model == nil {
		return startupPickerStatusProjection{}
	}
	projection := startupPickerStatusProjection{Notice: model.notice}
	var newest startupPickerStatusRecord
	found := false
	for key, record := range model.failures {
		if model.activeTab != nil && key.tab != *model.activeTab {
			continue
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
	text := strings.TrimSpace(projection.Notice.Text)
	isError := projection.Notice.Kind == startupPickerNoticeError
	if projection.Failure != nil {
		text = sessionPickerFailureOperatorText(projection.Failure.Kind)
		isError = true
	}
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
		Tab: tab.category, Operation: operation, Generation: generation, Kind: kind, Diagnostic: diagnostic,
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
