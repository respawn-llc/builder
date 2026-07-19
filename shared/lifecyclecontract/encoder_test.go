package lifecyclecontract

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"core/shared/invariant"
)

func TestEncoderBoundsMultibyteMarkdownAtUTF8Boundary(t *testing.T) {
	envelope, err := NewEnvelope(EnvelopeInput{
		Scope:      ScopeClient,
		Category:   CategoryTaskComplete,
		OccurredAt: time.Date(2026, time.July, 19, 18, 0, 0, 0, time.UTC),
		Details:    NewTaskCompleteDetails(strings.Repeat("界", 1366), true),
	})
	if err != nil {
		t.Fatalf("new envelope: %v", err)
	}

	raw, err := NewEncoder(invariant.NewPolicy(invariant.WithMode(invariant.ModeDiagnostic))).Encode(envelope)
	if err != nil {
		t.Fatalf("encode envelope: %v", err)
	}

	var got struct {
		Details struct {
			FinalAnswer string `json:"final_answer"`
		} `json:"details"`
		Truncation Truncation `json:"truncation"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("decode envelope: %v", err)
	}
	if !utf8.ValidString(got.Details.FinalAnswer) {
		t.Fatalf("final_answer is not valid UTF-8: %q", got.Details.FinalAnswer)
	}
	if gotBytes := len(got.Details.FinalAnswer); gotBytes > MarkdownSummaryLimitBytes {
		t.Fatalf("final_answer bytes = %d, want <= %d", gotBytes, MarkdownSummaryLimitBytes)
	}
	if want := strings.Repeat("界", 1365); got.Details.FinalAnswer != want {
		t.Fatalf("final_answer = %q, want UTF-8 prefix %q", got.Details.FinalAnswer, want)
	}
	if want := []TruncationField{TruncationFieldFinalAnswer}; !reflect.DeepEqual(got.Truncation.Fields, want) {
		t.Fatalf("truncation fields = %#v, want %#v", got.Truncation.Fields, want)
	}
}

func TestEncoderPreservesMultibyteMarkdownAtExactSummaryLimit(t *testing.T) {
	finalAnswer := strings.Repeat("😀", MarkdownSummaryLimitBytes/len("😀"))
	envelope, err := NewEnvelope(EnvelopeInput{
		Scope:      ScopeClient,
		Category:   CategoryTaskComplete,
		OccurredAt: time.Date(2026, time.July, 19, 18, 0, 0, 0, time.UTC),
		Details:    NewTaskCompleteDetails(finalAnswer, false),
	})
	if err != nil {
		t.Fatalf("new envelope: %v", err)
	}

	raw, err := NewEncoder(invariant.NewPolicy(invariant.WithMode(invariant.ModeDiagnostic))).Encode(envelope)
	if err != nil {
		t.Fatalf("encode envelope: %v", err)
	}
	var got struct {
		Details struct {
			FinalAnswer string `json:"final_answer"`
		} `json:"details"`
		Truncation *Truncation `json:"truncation"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("decode envelope: %v", err)
	}
	if got.Details.FinalAnswer != finalAnswer || len(got.Details.FinalAnswer) != MarkdownSummaryLimitBytes {
		t.Fatalf("final_answer bytes = %d, want preserved %d-byte source", len(got.Details.FinalAnswer), MarkdownSummaryLimitBytes)
	}
	if got.Truncation != nil {
		t.Fatalf("exact-limit summary unexpectedly declares truncation: %#v", got.Truncation)
	}
}

func TestLimitMarkdownSummaryPreservesUTF8AtByteBoundary(t *testing.T) {
	input := strings.Repeat("界", 1366)
	limited, truncated := LimitMarkdownSummary(input)
	if !truncated {
		t.Fatal("expected markdown summary truncation")
	}
	if !utf8.ValidString(limited) {
		t.Fatalf("limited markdown is not valid UTF-8: %q", limited)
	}
	if got, want := len(limited), MarkdownSummaryLimitBytes-1; got != want {
		t.Fatalf("limited markdown bytes = %d, want %d", got, want)
	}
	if got, want := limited, strings.Repeat("界", 1365); got != want {
		t.Fatalf("limited markdown = %q, want %q", got, want)
	}
}

func TestLimitMarkdownSummaryRejectsInvalidUTF8Source(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("invalid UTF-8 lifecycle markdown source did not fail fast")
		}
	}()
	_, _ = LimitMarkdownSummary(string([]byte{0xff}))
}

func TestEncoderBudgetsOptionalContextWithinDeterministicWholeObjectLimit(t *testing.T) {
	title := strings.Repeat("<", WholeObjectLimitBytes)
	envelope, err := NewEnvelope(EnvelopeInput{
		Scope:      ScopeClient,
		Category:   CategoryTaskComplete,
		OccurredAt: time.Date(2026, time.July, 19, 18, 0, 0, 0, time.UTC),
		Context:    Context{SessionTitle: &title},
		Details:    NewTaskCompleteDetails(strings.Repeat("<", MarkdownSummaryLimitBytes), false),
	})
	if err != nil {
		t.Fatalf("new envelope: %v", err)
	}

	encoder := NewEncoder(invariant.NewPolicy(invariant.WithMode(invariant.ModeDiagnostic)))
	first, err := encoder.Encode(envelope)
	if err != nil {
		t.Fatalf("first encode: %v", err)
	}
	second, err := encoder.Encode(envelope)
	if err != nil {
		t.Fatalf("second encode: %v", err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("repeated encodings differ:\nfirst:  %s\nsecond: %s", first, second)
	}
	if gotBytes := len(first); gotBytes > WholeObjectLimitBytes {
		t.Fatalf("whole payload bytes = %d, want <= %d", gotBytes, WholeObjectLimitBytes)
	}

	var got struct {
		Context struct {
			SessionTitle string `json:"session_title"`
		} `json:"context"`
		Truncation Truncation `json:"truncation"`
	}
	if err := json.Unmarshal(first, &got); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if !utf8.ValidString(got.Context.SessionTitle) {
		t.Fatalf("session_title is not valid UTF-8: %q", got.Context.SessionTitle)
	}
	if got.Context.SessionTitle == title || !strings.HasPrefix(title, got.Context.SessionTitle) {
		t.Fatalf("session_title = %q, want a bounded source prefix", got.Context.SessionTitle)
	}
	if want := []TruncationField{TruncationFieldSessionTitle}; !reflect.DeepEqual(got.Truncation.Fields, want) {
		t.Fatalf("truncation fields = %#v, want %#v", got.Truncation.Fields, want)
	}
}

func TestEncoderOmitsOptionalSessionTitleWhenNoTitleBytesFit(t *testing.T) {
	title := "😀"
	var taskIDLength int
	for low, high := 1, WholeObjectLimitBytes; low <= high; {
		middle := low + (high-low)/2
		envelope := sessionTitleBudgetEnvelope(t, strings.Repeat("<", middle), nil)
		raw, err := json.Marshal(envelope.wire())
		if err != nil {
			t.Fatalf("marshal title-omitted envelope: %v", err)
		}
		if len(raw) <= WholeObjectLimitBytes {
			taskIDLength = middle
			low = middle + 1
			continue
		}
		high = middle - 1
	}
	if taskIDLength == 0 {
		t.Fatal("could not construct a title-omitted payload within the limit")
	}
	withTitle := sessionTitleBudgetEnvelope(t, strings.Repeat("<", taskIDLength), &title)
	withTitleRaw, err := json.Marshal(withTitle.wire())
	if err != nil {
		t.Fatalf("marshal title-present envelope: %v", err)
	}
	if len(withTitleRaw) <= WholeObjectLimitBytes {
		t.Fatalf("title-present payload bytes = %d, want overflow", len(withTitleRaw))
	}
	envelope := sessionTitleBudgetEnvelope(t, strings.Repeat("<", taskIDLength), &title)

	raw, err := NewEncoder(invariant.NewPolicy(invariant.WithMode(invariant.ModeDiagnostic))).Encode(envelope)
	if err != nil {
		t.Fatalf("encode envelope: %v", err)
	}
	if len(raw) > WholeObjectLimitBytes {
		t.Fatalf("payload bytes = %d, want <= %d", len(raw), WholeObjectLimitBytes)
	}
	var got struct {
		Context    map[string]any `json:"context"`
		Truncation Truncation     `json:"truncation"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if _, exists := got.Context["session_title"]; exists {
		t.Fatalf("budgeted context retains session_title: %#v", got.Context)
	}
	if want := []TruncationField{TruncationFieldSessionTitle}; !reflect.DeepEqual(got.Truncation.Fields, want) {
		t.Fatalf("truncation fields = %#v, want %#v", got.Truncation.Fields, want)
	}
}

func sessionTitleBudgetEnvelope(t *testing.T, workflowTaskRaw string, title *string) Envelope {
	t.Helper()
	taskID, err := ParseWorkflowTaskID(workflowTaskRaw)
	if err != nil {
		t.Fatalf("parse workflow task ID: %v", err)
	}
	envelope, err := NewEnvelope(EnvelopeInput{
		Scope:      ScopeClient,
		Category:   CategoryTaskComplete,
		OccurredAt: time.Date(2026, time.July, 19, 18, 0, 0, 0, time.UTC),
		Context: Context{
			SessionTitle:   title,
			WorkflowTaskID: &taskID,
		},
		Details: NewTaskCompleteDetails("Done.", false),
		Truncation: &Truncation{Fields: []TruncationField{
			TruncationFieldSessionTitle,
		}},
	})
	if err != nil {
		t.Fatalf("new envelope: %v", err)
	}
	return envelope
}

func TestEncoderRecordsTypedTruncationForEveryMarkdownSummaryVariant(t *testing.T) {
	occurredAt := time.Date(2026, time.July, 19, 18, 0, 0, 0, time.UTC)
	oversized := strings.Repeat("界", 1366)
	tests := []struct {
		name       string
		category   Category
		details    Details
		wantField  TruncationField
		summaryKey string
		wantSource string
	}{
		{
			name:       "task complete final answer",
			category:   CategoryTaskComplete,
			details:    NewTaskCompleteDetails(oversized, false),
			wantField:  TruncationFieldFinalAnswer,
			summaryKey: "final_answer",
			wantSource: oversized,
		},
		{
			name:       "task error diagnostic",
			category:   CategoryTaskError,
			details:    NewTaskErrorDetails(oversized),
			wantField:  TruncationFieldDiagnostic,
			summaryKey: "diagnostic",
			wantSource: oversized,
		},
		{
			name:       "input required summary",
			category:   CategoryInputRequired,
			details:    NewInputRequiredDetails(InputKindQuestion, oversized),
			wantField:  TruncationFieldInputSummary,
			summaryKey: "summary",
			wantSource: oversized,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			envelope, err := NewEnvelope(EnvelopeInput{
				Scope:      ScopeClient,
				Category:   test.category,
				OccurredAt: occurredAt,
				Details:    test.details,
			})
			if err != nil {
				t.Fatalf("new envelope: %v", err)
			}
			raw, err := NewEncoder(invariant.NewPolicy(invariant.WithMode(invariant.ModeDiagnostic))).Encode(envelope)
			if err != nil {
				t.Fatalf("encode envelope: %v", err)
			}
			var got struct {
				Details    map[string]any `json:"details"`
				Truncation Truncation     `json:"truncation"`
			}
			if err := json.Unmarshal(raw, &got); err != nil {
				t.Fatalf("decode envelope: %v", err)
			}
			if want := []TruncationField{test.wantField}; !reflect.DeepEqual(got.Truncation.Fields, want) {
				t.Fatalf("truncation fields = %#v, want %#v", got.Truncation.Fields, want)
			}
			summary, ok := got.Details[test.summaryKey].(string)
			if !ok {
				t.Fatalf("details[%q] = %T, want string", test.summaryKey, got.Details[test.summaryKey])
			}
			if !utf8.ValidString(summary) || len(summary) > MarkdownSummaryLimitBytes || !strings.HasPrefix(test.wantSource, summary) {
				t.Fatalf("bounded summary = %q, want UTF-8 bounded source prefix", summary)
			}
		})
	}
}

func TestEncodedWireSizeMatchesEncodingJSONForEverySchemaVariant(t *testing.T) {
	sessionID := sessionIDPtr(t, "session-<>&")
	workflowTaskID, err := ParseWorkflowTaskID("task-<>&")
	if err != nil {
		t.Fatalf("parse workflow task ID: %v", err)
	}
	escaped := "Markdown <>&\u2028\u2029\n\t\u0000😀"
	occurredAt := time.Date(2026, time.July, 19, 18, 0, 0, 123456789, time.FixedZone("CEST", 2*60*60))
	tests := []struct {
		name       string
		context    Context
		category   Category
		details    Details
		truncation *Truncation
	}{
		{
			name:     "session start with every optional context field",
			category: CategorySessionStart,
			context: Context{
				SessionID:      sessionID,
				SessionTitle:   stringPtr(escaped),
				WorkflowTaskID: &workflowTaskID,
			},
			details: NewSessionStartDetails(OpeningKindNew),
			truncation: &Truncation{Fields: []TruncationField{
				TruncationFieldSessionTitle,
			}},
		},
		{
			name:     "task complete with escaped final answer",
			category: CategoryTaskComplete,
			context: Context{
				SessionTitle:   stringPtr(escaped),
				WorkflowTaskID: &workflowTaskID,
			},
			details: NewTaskCompleteDetails(escaped, true),
			truncation: &Truncation{Fields: []TruncationField{
				TruncationFieldSessionTitle,
				TruncationFieldFinalAnswer,
			}},
		},
		{
			name:       "task error with escaped diagnostic",
			category:   CategoryTaskError,
			context:    Context{WorkflowTaskID: &workflowTaskID},
			details:    NewTaskErrorDetails(escaped),
			truncation: &Truncation{Fields: []TruncationField{TruncationFieldDiagnostic}},
		},
		{
			name:       "input required with escaped summary",
			category:   CategoryInputRequired,
			context:    Context{SessionTitle: stringPtr(escaped)},
			details:    NewInputRequiredDetails(InputKindQuestion, escaped),
			truncation: &Truncation{Fields: []TruncationField{TruncationFieldInputSummary}},
		},
		{
			name:     "resource limit without optional fields",
			category: CategoryResourceLimit,
			details:  NewResourceLimitDetails(escaped),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			envelope, err := NewEnvelope(EnvelopeInput{
				Scope:      ScopeClient,
				Category:   test.category,
				OccurredAt: occurredAt,
				Focused:    true,
				Context:    test.context,
				Details:    test.details,
				Truncation: test.truncation,
			})
			if err != nil {
				t.Fatalf("new envelope: %v", err)
			}
			raw, err := json.Marshal(envelope.wire())
			if err != nil {
				t.Fatalf("marshal wire: %v", err)
			}
			if got, want := encodedWireSize(envelope), len(raw); got != want {
				t.Fatalf("manual wire size = %d, encoding/json size = %d, payload=%s", got, want, raw)
			}
		})
	}
}

func TestEncoderReturnsTypedIssueAndRecordsFixedMetadataOverflow(t *testing.T) {
	taskID, err := ParseWorkflowTaskID(strings.Repeat("<", WholeObjectLimitBytes))
	if err != nil {
		t.Fatalf("parse workflow task ID: %v", err)
	}
	envelope, err := NewEnvelope(EnvelopeInput{
		Scope:      ScopeClient,
		Category:   CategoryTaskComplete,
		OccurredAt: time.Date(2026, time.July, 19, 18, 0, 0, 0, time.UTC),
		Context:    Context{WorkflowTaskID: &taskID},
		Details:    NewTaskCompleteDetails("Done.", false),
	})
	if err != nil {
		t.Fatalf("new envelope: %v", err)
	}

	var diagnostics []invariant.Diagnostic
	encoder := NewEncoder(invariant.NewPolicy(
		invariant.WithMode(invariant.ModeDiagnostic),
		invariant.WithSink(invariant.SinkFunc(func(diagnostic invariant.Diagnostic) {
			diagnostics = append(diagnostics, diagnostic)
		})),
	))
	raw, err := encoder.Encode(envelope)
	if raw != nil {
		t.Fatalf("overflow payload = %q, want no partial payload", raw)
	}
	var issue EncodingIssue
	if !errors.As(err, &issue) {
		t.Fatalf("overflow error = %T %v, want EncodingIssue", err, err)
	}
	if issue.Kind != EncodingIssueFixedMetadataOverflow || issue.FixedBytes <= WholeObjectLimitBytes || issue.LimitBytes != WholeObjectLimitBytes {
		t.Fatalf("encoding issue = %#v", issue)
	}
	if len(diagnostics) != 1 {
		t.Fatalf("diagnostics = %d, want 1", len(diagnostics))
	}
	diagnostic := diagnostics[0]
	if diagnostic.Scope != invariant.ScopeLifecycleEncoding {
		t.Fatalf("diagnostic scope = %q, want %q", diagnostic.Scope, invariant.ScopeLifecycleEncoding)
	}
	assertInvariantField(t, diagnostic, invariant.FieldOperation, "encode_lifecycle_envelope")
	assertInvariantField(t, diagnostic, invariant.FieldSchemaVersion, "1")
	assertInvariantField(t, diagnostic, invariant.FieldCategory, string(CategoryTaskComplete))
	assertInvariantField(t, diagnostic, invariant.FieldDetailVariant, "task_complete")
	assertInvariantField(t, diagnostic, invariant.FieldWholeObjectCapBytes, "32768")
	if diagnostic.Fields[invariant.FieldMeasuredFixedBytes] == "" || diagnostic.Fields[invariant.FieldFieldByteLengths] == "" {
		t.Fatalf("overflow diagnostic lacks measured bytes or field lengths: %#v", diagnostic.Fields)
	}
	if diagnostic.Stack == "" {
		t.Fatal("overflow diagnostic lacks stack")
	}
}

func TestEncoderFixedMetadataMeasurementIncludesPostTruncationMetadata(t *testing.T) {
	taskID, err := ParseWorkflowTaskID(strings.Repeat("<", WholeObjectLimitBytes))
	if err != nil {
		t.Fatalf("parse workflow task ID: %v", err)
	}
	envelope, err := NewEnvelope(EnvelopeInput{
		Scope:      ScopeClient,
		Category:   CategoryTaskComplete,
		OccurredAt: time.Date(2026, time.July, 19, 18, 0, 0, 0, time.UTC),
		Context:    Context{WorkflowTaskID: &taskID},
		Details:    NewTaskCompleteDetails(strings.Repeat("界", 1366), false),
	})
	if err != nil {
		t.Fatalf("new envelope: %v", err)
	}

	_, err = NewEncoder(invariant.NewPolicy(invariant.WithMode(invariant.ModeDiagnostic))).Encode(envelope)
	var issue EncodingIssue
	if !errors.As(err, &issue) {
		t.Fatalf("overflow error = %T %v, want EncodingIssue", err, err)
	}
	if issue.Kind != EncodingIssueFixedMetadataOverflow {
		t.Fatalf("encoding issue kind = %q, want %q", issue.Kind, EncodingIssueFixedMetadataOverflow)
	}

	expected := copyEnvelope(envelope)
	truncated := truncationSet(expected.truncation)
	limitMarkdownSummary(&expected, truncated)
	expected.truncation = canonicalTruncation(truncated)
	expected.details.taskComplete.FinalAnswer = ""
	expectedRaw, err := json.Marshal(expected.wire())
	if err != nil {
		t.Fatalf("marshal expected fixed metadata: %v", err)
	}
	if !reflect.DeepEqual(expected.truncation.Fields, []TruncationField{TruncationFieldFinalAnswer}) {
		t.Fatalf("post-truncation metadata = %#v, want final answer field", expected.truncation.Fields)
	}
	if len(expectedRaw) <= WholeObjectLimitBytes {
		t.Fatalf("fixed metadata bytes = %d, want overflow beyond %d", len(expectedRaw), WholeObjectLimitBytes)
	}
	if got, want := issue.FixedBytes, len(expectedRaw); got != want {
		t.Fatalf("fixed metadata bytes = %d, want %d including post-truncation metadata", got, want)
	}
}

func TestEncoderPanicsWithCompleteDiagnosticForFixedMetadataOverflowInDebugMode(t *testing.T) {
	taskID, err := ParseWorkflowTaskID(strings.Repeat("<", WholeObjectLimitBytes))
	if err != nil {
		t.Fatalf("parse workflow task ID: %v", err)
	}
	envelope, err := NewEnvelope(EnvelopeInput{
		Scope:      ScopeClient,
		Category:   CategoryTaskError,
		OccurredAt: time.Date(2026, time.July, 19, 18, 0, 0, 0, time.UTC),
		Context:    Context{WorkflowTaskID: &taskID},
		Details:    NewTaskErrorDetails("Agent stopped."),
	})
	if err != nil {
		t.Fatalf("new envelope: %v", err)
	}

	defer func() {
		recovered := recover()
		diagnostic, ok := recovered.(invariant.Diagnostic)
		if !ok {
			t.Fatalf("panic payload = %T, want invariant.Diagnostic", recovered)
		}
		if diagnostic.Scope != invariant.ScopeLifecycleEncoding {
			t.Fatalf("diagnostic scope = %q, want %q", diagnostic.Scope, invariant.ScopeLifecycleEncoding)
		}
		assertInvariantField(t, diagnostic, invariant.FieldOperation, "encode_lifecycle_envelope")
		assertInvariantField(t, diagnostic, invariant.FieldSchemaVersion, "1")
		assertInvariantField(t, diagnostic, invariant.FieldCategory, string(CategoryTaskError))
		assertInvariantField(t, diagnostic, invariant.FieldDetailVariant, "task_error")
		assertInvariantField(t, diagnostic, invariant.FieldWholeObjectCapBytes, "32768")
		if diagnostic.Fields[invariant.FieldMeasuredFixedBytes] == "" || diagnostic.Fields[invariant.FieldFieldByteLengths] == "" {
			t.Fatalf("overflow diagnostic lacks measured bytes or field lengths: %#v", diagnostic.Fields)
		}
		if diagnostic.Stack == "" {
			t.Fatal("overflow diagnostic lacks stack")
		}
	}()

	_, _ = NewEncoder(invariant.NewPolicy(invariant.WithMode(invariant.ModePanic))).Encode(envelope)
	t.Fatal("debug overflow did not panic")
}

func TestEnvelopeMarshalJSONIsReleaseSafeRegardlessOfAmbientInvariantMode(t *testing.T) {
	t.Setenv("KENT_INVARIANT_MODE", "panic")
	t.Setenv("KENT_DEBUG", "1")
	envelope := fixedMetadataOverflowEnvelope(t, CategoryTaskComplete)

	defer func() {
		if recovered := recover(); recovered != nil {
			t.Fatalf("json.Marshal panicked from ambient invariant mode: %T %v", recovered, recovered)
		}
	}()
	raw, err := json.Marshal(envelope)
	if raw != nil {
		t.Fatalf("overflow payload = %q, want no partial payload", raw)
	}
	if err == nil {
		t.Fatal("json.Marshal succeeded for fixed metadata overflow")
	}
}

func fixedMetadataOverflowEnvelope(t *testing.T, category Category) Envelope {
	t.Helper()
	taskID, err := ParseWorkflowTaskID(strings.Repeat("<", WholeObjectLimitBytes))
	if err != nil {
		t.Fatalf("parse workflow task ID: %v", err)
	}
	var details Details
	switch category {
	case CategoryTaskComplete:
		details = NewTaskCompleteDetails("Done.", false)
	case CategoryTaskError:
		details = NewTaskErrorDetails("Agent stopped.")
	default:
		t.Fatalf("unsupported overflow category %q", category)
	}
	envelope, err := NewEnvelope(EnvelopeInput{
		Scope:      ScopeClient,
		Category:   category,
		OccurredAt: time.Date(2026, time.July, 19, 18, 0, 0, 0, time.UTC),
		Context:    Context{WorkflowTaskID: &taskID},
		Details:    details,
	})
	if err != nil {
		t.Fatalf("new envelope: %v", err)
	}
	return envelope
}

func assertInvariantField(t *testing.T, diagnostic invariant.Diagnostic, field invariant.Field, want string) {
	t.Helper()
	if got := diagnostic.Fields[field]; got != want {
		t.Fatalf("diagnostic field %q = %q, want %q", field, got, want)
	}
}
