package session

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"core/shared/runtimeids"
)

func TestReviewerFactRecordsRoundTripWithExactSourceContent(t *testing.T) {
	stepID := "11111111-1111-4111-8111-111111111111"
	feedback := ReviewerFeedbackRecord{
		ID:          runtimeids.NewReviewerFeedbackID(),
		Suggestions: []string{"  **preserve**  \n\n- item", "second\nline  "},
		Visibility:  EntryVisibilityOngoingCollapsed,
	}
	reviewerError := ReviewerErrorRecord{
		ID:     runtimeids.NewReviewerErrorID(),
		Detail: "  raw failure detail\nwith Markdown  ",
	}

	for _, test := range []struct {
		name    string
		payload EventRecordPayload
		assert  func(t *testing.T, payload EventRecordPayload)
	}{
		{
			name:    "feedback",
			payload: feedback,
			assert: func(t *testing.T, payload EventRecordPayload) {
				got, ok := payload.(ReviewerFeedbackRecord)
				if !ok {
					t.Fatalf("payload type = %T, want ReviewerFeedbackRecord", payload)
				}
				if !reflect.DeepEqual(got, feedback) {
					t.Fatalf("feedback = %#v, want %#v", got, feedback)
				}
			},
		},
		{
			name:    "error",
			payload: reviewerError,
			assert: func(t *testing.T, payload EventRecordPayload) {
				got, ok := payload.(ReviewerErrorRecord)
				if !ok {
					t.Fatalf("payload type = %T, want ReviewerErrorRecord", payload)
				}
				if !reflect.DeepEqual(got, reviewerError) {
					t.Fatalf("error = %#v, want %#v", got, reviewerError)
				}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			record, err := NewEventRecord(1, &stepID, test.payload)
			if err != nil {
				t.Fatalf("create event record: %v", err)
			}
			line, err := encodeEventRecordV1(record)
			if err != nil {
				t.Fatalf("encode event record: %v", err)
			}
			decoded, err := decodeEventRecordV1(line)
			if err != nil {
				t.Fatalf("decode event record: %v", err)
			}
			test.assert(t, mustEventRecordPayload(decoded))
		})
	}
}

func TestReviewerFeedbackRecordRejectsInvalidValues(t *testing.T) {
	stepID := "11111111-1111-4111-8111-111111111111"
	tests := []ReviewerFeedbackRecord{
		{ID: runtimeids.NewReviewerFeedbackID(), Visibility: EntryVisibilityOngoing},
		{ID: runtimeids.NewReviewerFeedbackID(), Suggestions: []string{""}, Visibility: EntryVisibilityOngoing},
		{ID: runtimeids.NewReviewerFeedbackID(), Suggestions: []string{" \n\t "}, Visibility: EntryVisibilityOngoing},
		{ID: runtimeids.NewReviewerFeedbackID(), Suggestions: []string{"suggestion"}, Visibility: EntryVisibilityAuto},
		{Suggestions: []string{"suggestion"}, Visibility: EntryVisibilityOngoing},
	}
	for _, payload := range tests {
		if _, err := NewEventRecord(1, &stepID, payload); err == nil {
			t.Fatalf("accepted invalid Reviewer feedback: %#v", payload)
		}
	}
}

func TestReviewerErrorRecordRejectsInvalidValues(t *testing.T) {
	stepID := "11111111-1111-4111-8111-111111111111"
	tests := []ReviewerErrorRecord{
		{ID: runtimeids.NewReviewerErrorID()},
		{ID: runtimeids.NewReviewerErrorID(), Detail: ""},
		{ID: runtimeids.NewReviewerErrorID(), Detail: " \n\t "},
		{Detail: "failure"},
	}
	for _, payload := range tests {
		if _, err := NewEventRecord(1, &stepID, payload); err == nil {
			t.Fatalf("accepted invalid Reviewer error: %#v", payload)
		}
	}
}

func TestReviewerFactsRequireStepIdentity(t *testing.T) {
	_, err := NewEventRecord(1, nil, ReviewerFeedbackRecord{
		ID: runtimeids.NewReviewerFeedbackID(), Suggestions: []string{"suggestion"}, Visibility: EntryVisibilityOngoing,
	})
	if err == nil {
		t.Fatal("accepted Reviewer feedback without enclosing step identity")
	}
	_, err = NewEventRecord(1, nil, ReviewerErrorRecord{
		ID: runtimeids.NewReviewerErrorID(), Detail: "failure",
	})
	if err == nil {
		t.Fatal("accepted Reviewer error without enclosing step identity")
	}
}

func TestReviewerPointerPayloadsNormalizeToCopiedValues(t *testing.T) {
	stepID := "11111111-1111-4111-8111-111111111111"
	feedback := &ReviewerFeedbackRecord{
		ID:          runtimeids.NewReviewerFeedbackID(),
		Suggestions: []string{"original"},
		Visibility:  EntryVisibilityOngoing,
	}
	record, err := NewEventRecord(1, &stepID, feedback)
	if err != nil {
		t.Fatalf("create pointer Reviewer feedback record: %v", err)
	}
	feedback.Suggestions[0] = "mutated after validation"
	payload, err := record.Payload()
	if err != nil {
		t.Fatalf("read normalized pointer payload: %v", err)
	}
	got, ok := payload.(ReviewerFeedbackRecord)
	if !ok || len(got.Suggestions) != 1 || got.Suggestions[0] != "original" {
		t.Fatalf("normalized pointer payload = %#v", payload)
	}
}

func TestReviewerFactIDsRejectNonCanonicalJSONUUIDs(t *testing.T) {
	for _, raw := range []string{
		" 11111111-1111-4111-8111-111111111111",
		"11111111-1111-4111-8111-111111111111 ",
		"11111111-1111-3111-8111-111111111111",
		"11111111-1111-4111-7111-111111111111",
		strings.ToUpper("abcdefab-cdef-4abc-8def-abcdefabcdef"),
	} {
		data, err := json.Marshal(raw)
		if err != nil {
			t.Fatal(err)
		}
		var id runtimeids.ReviewerFeedbackID
		if err := json.Unmarshal(data, &id); err == nil {
			t.Fatalf("accepted noncanonical Reviewer feedback ID %q", raw)
		}
	}
}
