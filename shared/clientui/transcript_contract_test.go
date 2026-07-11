package clientui

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestTranscriptMessageContractWireValues(t *testing.T) {
	messageKinds := map[TranscriptMessageKind]string{
		TranscriptMessageHydration:                   "hydration",
		TranscriptMessageCommittedRow:                "committed_row",
		TranscriptMessageAssistantDelta:              "assistant_delta",
		TranscriptMessageAssistantStreamAbort:        "assistant_stream_abort",
		TranscriptMessageToolStart:                   "tool_start",
		TranscriptMessageToolAbort:                   "tool_abort",
		TranscriptMessageQueuedOrSteeredMessageState: "queued_or_steered_message_state",
		TranscriptMessageRunState:                    "run_state",
		TranscriptMessageRuntimeActivity:             "runtime_activity",
		TranscriptMessageInputReconciliation:         "input_reconciliation",
		TranscriptMessageSessionStatus:               "session_status",
		TranscriptMessageSessionIdentity:             "session_identity",
		TranscriptMessageCompactionStatus:            "compaction_status",
		TranscriptMessageContextUsage:                "context_usage",
		TranscriptMessageGoalStatus:                  "goal_status",
		TranscriptMessageBackgroundActivity:          "background_activity",
		TranscriptMessagePendingSessionPrompt:        "pending_session_prompt",
	}
	for kind, want := range messageKinds {
		if got := string(kind); got != want {
			t.Fatalf("transcript message kind %q wire value = %q, want %q", kind, got, want)
		}
	}

	rowKinds := map[TranscriptRowKind]string{
		TranscriptRowUser:      "user",
		TranscriptRowAssistant: "assistant",
		TranscriptRowTool:      "tool",
		TranscriptRowNotice:    "notice",
	}
	for kind, want := range rowKinds {
		if got := string(kind); got != want {
			t.Fatalf("transcript row kind %q wire value = %q, want %q", kind, got, want)
		}
	}

	noticeSeverities := map[TranscriptNoticeSeverity]string{
		TranscriptNoticeInfo:    "info",
		TranscriptNoticeWarning: "warning",
		TranscriptNoticeError:   "error",
	}
	for severity, want := range noticeSeverities {
		if got := string(severity); got != want {
			t.Fatalf("transcript notice severity %q wire value = %q, want %q", severity, got, want)
		}
	}
}

func TestMessagePhaseNormalization(t *testing.T) {
	tests := map[string]MessagePhase{
		"commentary":   MessagePhaseCommentary,
		"COMMENTARY":   MessagePhaseCommentary,
		"final_answer": MessagePhaseFinal,
		"finalanswer":  MessagePhaseFinal,
		"final":        MessagePhaseFinal,
		"unknown":      "",
		"":             "",
	}
	for raw, want := range tests {
		if got := NormalizeMessagePhase(raw); got != want {
			t.Fatalf("NormalizeMessagePhase(%q) = %q, want %q", raw, got, want)
		}
	}
}

func TestTranscriptMessageContractJSONRoundTrip(t *testing.T) {
	streamID := uuid.New()
	input := TranscriptMessage{
		Sequence: 2,
		Kind:     TranscriptMessageAssistantDelta,
		AssistantDelta: &TranscriptAssistantDelta{
			StreamID: streamID,
			Delta:    "hello",
			Phase:    MessagePhaseFinal,
		},
	}
	encoded, err := json.Marshal(input)
	if err != nil {
		t.Fatalf("marshal transcript message: %v", err)
	}

	var decoded TranscriptMessage
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("unmarshal transcript message: %v", err)
	}
	if decoded.AssistantDelta == nil || decoded.AssistantDelta.StreamID != streamID || decoded.AssistantDelta.Delta != "hello" {
		t.Fatalf("decoded transcript message = %#v, want assistant delta with uuid stream", decoded)
	}
}

func TestTranscriptMessageHasExactlyOnePayloadForKind(t *testing.T) {
	valid := TranscriptMessage{
		Kind:      TranscriptMessageHydration,
		Hydration: &TranscriptHydration{},
	}
	if err := valid.ValidatePayload(); err != nil {
		t.Fatalf("valid transcript message payload rejected: %v", err)
	}

	missing := TranscriptMessage{Kind: TranscriptMessageHydration}
	if err := missing.ValidatePayload(); err == nil {
		t.Fatal("missing transcript message payload accepted")
	}

	ambiguous := TranscriptMessage{
		Kind:         TranscriptMessageHydration,
		Hydration:    &TranscriptHydration{},
		CommittedRow: &TranscriptCommittedRow{},
	}
	if err := ambiguous.ValidatePayload(); err == nil {
		t.Fatal("transcript message with multiple payloads accepted")
	}
}

func TestTranscriptDTOsDoNotReferenceLegacyShapes(t *testing.T) {
	forbidden := map[reflect.Type]struct{}{
		reflect.TypeOf(RuntimeMainView{}):            {},
		reflect.TypeOf(PendingPromptEvent{}):         {},
		reflect.TypeOf(AttentionNotificationEvent{}): {},
	}
	for _, typ := range transcriptContractTypes() {
		walkType(t, typ, map[reflect.Type]struct{}{}, func(current reflect.Type, path string) {
			if _, ok := forbidden[current]; ok {
				t.Fatalf("transcript DTO %s references forbidden legacy shape %s", path, current)
			}
		})
	}
}

func TestTranscriptDTOsDoNotExposeLegacyCoordinatesOrGenericEscapes(t *testing.T) {
	for _, typ := range transcriptContractTypes() {
		walkType(t, typ, map[reflect.Type]struct{}{}, func(current reflect.Type, path string) {
			if current.Kind() == reflect.Map {
				t.Fatalf("transcript DTO %s exposes generic map payload", path)
			}
			if current.Kind() != reflect.Struct {
				return
			}
			for i := 0; i < current.NumField(); i++ {
				field := current.Field(i)
				if field.PkgPath != "" {
					continue
				}
				name := strings.ToLower(field.Name)
				switch {
				case name == "role":
					t.Fatalf("transcript DTO %s.%s exposes legacy role", path, field.Name)
				case strings.Contains(name, "revision"),
					strings.Contains(name, "cursor"),
					strings.Contains(name, "offset"),
					strings.Contains(name, "range"),
					strings.Contains(name, "committedentrycount"),
					strings.Contains(name, "totalentries"):
					t.Fatalf("transcript DTO %s.%s exposes legacy transcript coordinate", path, field.Name)
				case name == "message" || name == "displaymessage":
					t.Fatalf("transcript DTO %s.%s exposes generic server display message", path, field.Name)
				}
			}
		})
	}
}

func TestTranscriptAssistantStreamIdentityUsesUUIDValues(t *testing.T) {
	uuidType := reflect.TypeOf(uuid.UUID{})
	streamIDField, ok := reflect.TypeOf(TranscriptAssistantRow{}).FieldByName("StreamID")
	if !ok {
		t.Fatal("TranscriptAssistantRow.StreamID field not found")
	}
	tests := map[string]reflect.Type{
		"TranscriptAssistantStream.StreamID":      reflect.TypeOf(TranscriptAssistantStream{}).Field(0).Type,
		"TranscriptAssistantDelta.StreamID":       reflect.TypeOf(TranscriptAssistantDelta{}).Field(0).Type,
		"TranscriptAssistantStreamAbort.StreamID": reflect.TypeOf(TranscriptAssistantStreamAbort{}).Field(0).Type,
		"TranscriptAssistantRow.StreamID":         streamIDField.Type,
	}
	for name, typ := range tests {
		if typ == uuidType {
			continue
		}
		if typ.Kind() == reflect.Pointer && typ.Elem() == uuidType {
			continue
		}
		t.Fatalf("%s type = %s, want uuid.UUID or *uuid.UUID", name, typ)
	}
}

func transcriptContractTypes() []reflect.Type {
	return []reflect.Type{
		reflect.TypeOf(TranscriptMessage{}),
		reflect.TypeOf(TranscriptHydration{}),
		reflect.TypeOf(TranscriptCommittedRow{}),
		reflect.TypeOf(TranscriptUserRow{}),
		reflect.TypeOf(TranscriptAssistantRow{}),
		reflect.TypeOf(TranscriptToolRow{}),
		reflect.TypeOf(TranscriptNoticeRow{}),
		reflect.TypeOf(TranscriptNoticeData{}),
		reflect.TypeOf(TranscriptCacheWarningData{}),
		reflect.TypeOf(TranscriptDiagnosticData{}),
		reflect.TypeOf(TranscriptAssistantStream{}),
		reflect.TypeOf(TranscriptAssistantDelta{}),
		reflect.TypeOf(TranscriptAssistantStreamAbort{}),
		reflect.TypeOf(TranscriptToolStart{}),
		reflect.TypeOf(TranscriptToolAbort{}),
		reflect.TypeOf(TranscriptQueuedOrSteeredMessageState{}),
		reflect.TypeOf(TranscriptSessionStatus{}),
		reflect.TypeOf(TranscriptSessionIdentity{}),
		reflect.TypeOf(TranscriptCompactionStatus{}),
		reflect.TypeOf(TranscriptGoalStatus{}),
		reflect.TypeOf(TranscriptBackgroundActivity{}),
		reflect.TypeOf(TranscriptPendingSessionPrompt{}),
		reflect.TypeOf(TranscriptPendingSessionPromptData{}),
	}
}

func walkType(t *testing.T, typ reflect.Type, seen map[reflect.Type]struct{}, visit func(reflect.Type, string)) {
	t.Helper()
	typ = dereferenceType(typ)
	if typ == nil {
		return
	}
	if _, ok := seen[typ]; ok {
		return
	}
	seen[typ] = struct{}{}
	visit(typ, typ.String())
	if typ.Kind() != reflect.Struct {
		return
	}
	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)
		if field.PkgPath != "" {
			continue
		}
		fieldType := dereferenceType(field.Type)
		visit(fieldType, typ.String()+"."+field.Name)
		if fieldType == nil || fieldType.PkgPath() != "core/shared/clientui" {
			continue
		}
		walkType(t, fieldType, seen, visit)
	}
}

func dereferenceType(typ reflect.Type) reflect.Type {
	for typ != nil {
		switch typ.Kind() {
		case reflect.Pointer, reflect.Slice, reflect.Array:
			typ = typ.Elem()
		default:
			return typ
		}
	}
	return nil
}
