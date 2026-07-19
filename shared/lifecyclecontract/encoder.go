package lifecyclecontract

import (
	"encoding/json"
	"fmt"
	"strconv"
	"unicode/utf8"

	"core/shared/invariant"
	"core/shared/textutil"
)

const (
	MarkdownSummaryLimitBytes = textutil.MarkdownSummaryLimitBytes
	WholeObjectLimitBytes     = 32 * 1024
)

type EncodingIssueKind string

const EncodingIssueFixedMetadataOverflow EncodingIssueKind = "fixed_metadata_overflow"

const EncodingIssueWholeObjectOverflow EncodingIssueKind = "whole_object_overflow"

type EncodingIssue struct {
	Kind       EncodingIssueKind
	FixedBytes int
	LimitBytes int
}

func (issue EncodingIssue) Error() string {
	if issue.Kind == EncodingIssueWholeObjectOverflow {
		return fmt.Sprintf(
			"lifecycle payload exceeds the %d-byte whole-object limit after budgeting",
			issue.LimitBytes,
		)
	}
	return fmt.Sprintf(
		"lifecycle payload %s: fixed metadata is %d bytes and exceeds the %d-byte limit",
		issue.Kind,
		issue.FixedBytes,
		issue.LimitBytes,
	)
}

type Encoder struct {
	invariants invariant.Policy
}

func NewEncoder(invariants invariant.Policy) Encoder {
	return Encoder{invariants: invariants}
}

func (encoder Encoder) Encode(envelope Envelope) ([]byte, error) {
	if err := envelope.Validate(); err != nil {
		return nil, err
	}
	if err := validateEnvelopeUTF8(envelope); err != nil {
		return nil, err
	}

	prepared := copyEnvelope(envelope)
	truncated := truncationSet(prepared.truncation)
	limitMarkdownSummary(&prepared, truncated)
	limitSessionTitleSource(&prepared, truncated)
	prepared.truncation = canonicalTruncation(truncated)

	if encodedWireSize(prepared) > WholeObjectLimitBytes && prepared.context.SessionTitle != nil {
		truncated[TruncationFieldSessionTitle] = struct{}{}
		prepared.truncation = canonicalTruncation(truncated)
		if !budgetSessionTitle(&prepared) {
			return nil, encoder.overflowIssue(prepared)
		}
	}

	if encodedWireSize(prepared) > WholeObjectLimitBytes {
		return nil, encoder.overflowIssue(prepared)
	}
	if err := prepared.Validate(); err != nil {
		return nil, err
	}

	encoded, err := json.Marshal(prepared.wire())
	if err != nil {
		return nil, err
	}
	if len(encoded) > WholeObjectLimitBytes {
		return nil, encoder.overflowIssue(prepared)
	}
	return encoded, nil
}

func (encoder Encoder) overflowIssue(envelope Envelope) error {
	fixedBytes := fixedMetadataSize(envelope)
	diagnostic := invariant.LifecycleEncodingDiagnostic(invariant.LifecycleEncodingDiagnosticInput{
		Operation:       "encode_lifecycle_envelope",
		SchemaVersion:   SchemaVersion,
		Category:        string(envelope.category),
		DetailVariant:   envelope.details.variantName(),
		FixedBytes:      fixedBytes,
		WholeObjectCap:  WholeObjectLimitBytes,
		FieldByteLength: lifecycleFieldByteLengths(envelope),
	})
	encoder.invariants.Check(false, diagnostic)
	kind := EncodingIssueWholeObjectOverflow
	if fixedBytes > WholeObjectLimitBytes {
		kind = EncodingIssueFixedMetadataOverflow
	}
	return EncodingIssue{
		Kind:       kind,
		FixedBytes: fixedBytes,
		LimitBytes: WholeObjectLimitBytes,
	}
}

func copyEnvelope(envelope Envelope) Envelope {
	return Envelope{
		scope:              envelope.scope,
		category:           envelope.category,
		compatibilityAlias: envelope.compatibilityAlias,
		occurredAt:         envelope.occurredAt,
		focused:            envelope.focused,
		context:            copyContext(envelope.context),
		details:            copyDetails(envelope.details),
		truncation:         copyTruncation(envelope.truncation),
	}
}

func copyDetails(details Details) Details {
	switch details.kind {
	case detailKindSessionStart:
		return NewSessionStartDetails(details.sessionStart.Kind)
	case detailKindTaskComplete:
		return NewTaskCompleteDetails(details.taskComplete.FinalAnswer, details.taskComplete.WorkPerformed)
	case detailKindTaskError:
		return NewTaskErrorDetails(details.taskError.Diagnostic)
	case detailKindInputRequired:
		return NewInputRequiredDetails(details.inputRequired.Kind, details.inputRequired.Summary)
	case detailKindResourceLimit:
		return NewResourceLimitDetails(details.resourceLimit.CompactionMode)
	default:
		return Details{}
	}
}

func validateEnvelopeUTF8(envelope Envelope) error {
	validate := func(field string, value string) error {
		if !utf8.ValidString(value) {
			return fmt.Errorf("%s must be valid UTF-8", field)
		}
		return nil
	}
	if envelope.context.SessionID != nil {
		if err := validate("context session_id", envelope.context.SessionID.String()); err != nil {
			return err
		}
	}
	if envelope.context.SessionTitle != nil {
		if err := validate("context session_title", *envelope.context.SessionTitle); err != nil {
			return err
		}
	}
	if envelope.context.WorkflowTaskID != nil {
		if err := validate("context workflow_task_id", envelope.context.WorkflowTaskID.String()); err != nil {
			return err
		}
	}
	switch envelope.details.kind {
	case detailKindTaskComplete:
		return validate("details final_answer", envelope.details.taskComplete.FinalAnswer)
	case detailKindTaskError:
		return validate("details diagnostic", envelope.details.taskError.Diagnostic)
	case detailKindInputRequired:
		return validate("details summary", envelope.details.inputRequired.Summary)
	case detailKindResourceLimit:
		return validate("details compaction_mode", envelope.details.resourceLimit.CompactionMode)
	default:
		return nil
	}
}

func truncationSet(truncation *Truncation) map[TruncationField]struct{} {
	fields := make(map[TruncationField]struct{})
	if truncation == nil {
		return fields
	}
	for _, field := range truncation.Fields {
		fields[field] = struct{}{}
	}
	return fields
}

func canonicalTruncation(fields map[TruncationField]struct{}) *Truncation {
	if len(fields) == 0 {
		return nil
	}
	ordered := make([]TruncationField, 0, len(fields))
	for _, field := range []TruncationField{
		TruncationFieldSessionTitle,
		TruncationFieldFinalAnswer,
		TruncationFieldDiagnostic,
		TruncationFieldInputSummary,
	} {
		if _, ok := fields[field]; ok {
			ordered = append(ordered, field)
		}
	}
	return &Truncation{Fields: ordered}
}

func limitMarkdownSummary(envelope *Envelope, truncated map[TruncationField]struct{}) {
	switch envelope.details.kind {
	case detailKindTaskComplete:
		limited, didTruncate := LimitMarkdownSummary(envelope.details.taskComplete.FinalAnswer)
		envelope.details.taskComplete.FinalAnswer = limited
		if didTruncate {
			truncated[TruncationFieldFinalAnswer] = struct{}{}
		}
	case detailKindTaskError:
		limited, didTruncate := truncateUTF8(envelope.details.taskError.Diagnostic, MarkdownSummaryLimitBytes)
		envelope.details.taskError.Diagnostic = limited
		if didTruncate {
			truncated[TruncationFieldDiagnostic] = struct{}{}
		}
	case detailKindInputRequired:
		limited, didTruncate := truncateUTF8(envelope.details.inputRequired.Summary, MarkdownSummaryLimitBytes)
		envelope.details.inputRequired.Summary = limited
		if didTruncate {
			truncated[TruncationFieldInputSummary] = struct{}{}
		}
	}
}

// LimitMarkdownSummary returns a valid UTF-8 prefix that fits the lifecycle
// Markdown-summary byte cap and reports whether source content was removed.
func LimitMarkdownSummary(value string) (string, bool) {
	limited, truncated, err := textutil.LimitUTF8Bytes(value, MarkdownSummaryLimitBytes)
	if err != nil {
		panic(fmt.Sprintf("limit lifecycle markdown summary: %v", err))
	}
	return limited, truncated
}

func limitSessionTitleSource(envelope *Envelope, truncated map[TruncationField]struct{}) {
	if envelope.context.SessionTitle == nil {
		return
	}
	limited, didTruncate := truncateUTF8(*envelope.context.SessionTitle, WholeObjectLimitBytes)
	envelope.context.SessionTitle = &limited
	if didTruncate {
		truncated[TruncationFieldSessionTitle] = struct{}{}
	}
}

func budgetSessionTitle(envelope *Envelope) bool {
	source := *envelope.context.SessionTitle
	envelope.context.SessionTitle = nil
	if encodedWireSize(*envelope) <= WholeObjectLimitBytes {
		return true
	}

	low, high := 1, len(source)
	best := -1
	for low <= high {
		middle := low + (high-low)/2
		candidate, _ := truncateUTF8(source, middle)
		envelope.context.SessionTitle = &candidate
		if encodedWireSize(*envelope) <= WholeObjectLimitBytes {
			best = len(candidate)
			low = middle + 1
			continue
		}
		high = middle - 1
	}
	if best <= 0 {
		return false
	}
	title := source[:best]
	envelope.context.SessionTitle = &title
	return true
}

func truncateUTF8(value string, limit int) (string, bool) {
	if len(value) <= limit {
		return value, false
	}
	for end := limit; end > 0; end-- {
		if utf8.ValidString(value[:end]) {
			return value[:end], true
		}
	}
	return "", true
}

func fixedMetadataSize(envelope Envelope) int {
	fixed := copyEnvelope(envelope)
	fixed.context.SessionTitle = nil
	switch fixed.details.kind {
	case detailKindTaskComplete:
		fixed.details.taskComplete.FinalAnswer = ""
	case detailKindTaskError:
		fixed.details.taskError.Diagnostic = ""
	case detailKindInputRequired:
		fixed.details.inputRequired.Summary = ""
	}
	return encodedWireSize(fixed)
}

func lifecycleFieldByteLengths(envelope Envelope) string {
	sessionID := ""
	if envelope.context.SessionID != nil {
		sessionID = envelope.context.SessionID.String()
	}
	lengths := []struct {
		name  string
		value string
	}{
		{name: "context.session_id", value: sessionID},
		{name: "context.session_title", value: optionalString(envelope.context.SessionTitle)},
		{name: "context.workflow_task_id", value: optionalWorkflowTaskID(envelope.context.WorkflowTaskID)},
	}
	switch envelope.details.kind {
	case detailKindTaskComplete:
		lengths = append(lengths, struct {
			name  string
			value string
		}{name: "details.final_answer", value: envelope.details.taskComplete.FinalAnswer})
	case detailKindTaskError:
		lengths = append(lengths, struct {
			name  string
			value string
		}{name: "details.diagnostic", value: envelope.details.taskError.Diagnostic})
	case detailKindInputRequired:
		lengths = append(lengths, struct {
			name  string
			value string
		}{name: "details.summary", value: envelope.details.inputRequired.Summary})
	case detailKindResourceLimit:
		lengths = append(lengths, struct {
			name  string
			value string
		}{name: "details.compaction_mode", value: envelope.details.resourceLimit.CompactionMode})
	}
	result := ""
	for index, length := range lengths {
		if index > 0 {
			result += ","
		}
		result += length.name + "=" + strconv.Itoa(len(length.value))
	}
	return result
}

func optionalString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func optionalWorkflowTaskID(value *WorkflowTaskID) string {
	if value == nil {
		return ""
	}
	return value.String()
}

func encodedWireSize(envelope Envelope) int {
	wire := envelope.wire()
	size := 2
	fields := []int{
		jsonFieldSize("schema_version", jsonIntegerSize(SchemaVersion)),
		jsonFieldSize("cesp_version", jsonStringSize(CESPVersion)),
		jsonFieldSize("scope", jsonStringSize(string(wire.Scope))),
		jsonFieldSize("category", jsonStringSize(string(wire.Category))),
		jsonFieldSize("hook_event_name", jsonStringSize(string(wire.HookEventName))),
		jsonFieldSize("occurred_at", jsonStringSize(envelope.occurredAt.Format(timeFormatRFC3339Nano))),
		jsonFieldSize("focused", jsonBooleanSize(wire.Focused)),
		jsonFieldSize("context", contextJSONSize(wire.Context)),
		jsonFieldSize("details", detailsJSONSize(envelope.details)),
	}
	if wire.Truncation != nil {
		fields = append(fields, jsonFieldSize("truncation", truncationJSONSize(*wire.Truncation)))
	}
	for index, field := range fields {
		if index > 0 {
			size++
		}
		size += field
	}
	return size
}

const timeFormatRFC3339Nano = "2006-01-02T15:04:05.999999999Z07:00"

func contextJSONSize(context Context) int {
	fields := make([]int, 0, 3)
	if context.SessionID != nil {
		fields = append(fields, jsonFieldSize("session_id", jsonStringSize(context.SessionID.String())))
	}
	if context.SessionTitle != nil {
		fields = append(fields, jsonFieldSize("session_title", jsonStringSize(*context.SessionTitle)))
	}
	if context.WorkflowTaskID != nil {
		fields = append(fields, jsonFieldSize("workflow_task_id", jsonStringSize(context.WorkflowTaskID.String())))
	}
	return jsonObjectSize(fields)
}

func detailsJSONSize(details Details) int {
	switch details.kind {
	case detailKindSessionStart:
		return jsonObjectSize([]int{jsonFieldSize("kind", jsonStringSize(string(details.sessionStart.Kind)))})
	case detailKindTaskComplete:
		return jsonObjectSize([]int{
			jsonFieldSize("final_answer", jsonStringSize(details.taskComplete.FinalAnswer)),
			jsonFieldSize("work_performed", jsonBooleanSize(details.taskComplete.WorkPerformed)),
		})
	case detailKindTaskError:
		return jsonObjectSize([]int{jsonFieldSize("diagnostic", jsonStringSize(details.taskError.Diagnostic))})
	case detailKindInputRequired:
		return jsonObjectSize([]int{
			jsonFieldSize("kind", jsonStringSize(string(details.inputRequired.Kind))),
			jsonFieldSize("summary", jsonStringSize(details.inputRequired.Summary)),
		})
	case detailKindResourceLimit:
		return jsonObjectSize([]int{jsonFieldSize("compaction_mode", jsonStringSize(details.resourceLimit.CompactionMode))})
	default:
		return 0
	}
}

func truncationJSONSize(truncation Truncation) int {
	values := 2
	for index, field := range truncation.Fields {
		if index > 0 {
			values++
		}
		values += jsonStringSize(string(field))
	}
	return jsonObjectSize([]int{jsonFieldSize("fields", values)})
}

func jsonObjectSize(fields []int) int {
	size := 2
	for index, field := range fields {
		if index > 0 {
			size++
		}
		size += field
	}
	return size
}

func jsonFieldSize(name string, valueSize int) int {
	return jsonStringSize(name) + 1 + valueSize
}

func jsonStringSize(value string) int {
	size := 2
	for _, character := range value {
		switch character {
		case '\\', '"':
			size += 2
		case '\b', '\t', '\n', '\f', '\r':
			size += 2
		case '<', '>', '&', '\u2028', '\u2029':
			size += 6
		default:
			if character < 0x20 {
				size += 6
			} else {
				size += utf8.RuneLen(character)
			}
		}
	}
	return size
}

func jsonIntegerSize(value int) int {
	return len(strconv.Itoa(value))
}

func jsonBooleanSize(value bool) int {
	if value {
		return len("true")
	}
	return len("false")
}
