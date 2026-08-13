package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"testing"
	"time"

	"core/shared/serverapi"
	"core/shared/sessionenv"
	"core/shared/transcript"

	"github.com/google/uuid"
)

type questionHistoryScriptSubscription struct {
	events []serverapi.QuestionHistoryEvent
	err    error
	closed bool
}

func (s *questionHistoryScriptSubscription) Next(context.Context) (serverapi.QuestionHistoryEvent, error) {
	if len(s.events) > 0 {
		event := s.events[0]
		s.events = s.events[1:]
		return event, nil
	}
	if s.err != nil {
		err := s.err
		s.err = nil
		return serverapi.QuestionHistoryEvent{}, err
	}
	return serverapi.QuestionHistoryEvent{}, io.EOF
}

func (s *questionHistoryScriptSubscription) Close() error {
	s.closed = true
	return nil
}

func TestQuestionHistoryHumanStreamingRetainsPartialOutputOnFailure(t *testing.T) {
	sub := &questionHistoryScriptSubscription{
		events: []serverapi.QuestionHistoryEvent{
			{Kind: serverapi.QuestionHistoryEventStarted, LargeHistory: boolPointer(false)},
			{
				Kind: serverapi.QuestionHistoryEventQuestion,
				Question: &serverapi.QuestionHistoryQuestion{
					Question: "opaque-question",
					Answer:   "opaque-answer",
				},
			},
		},
		err: errors.New("terminal failure"),
	}
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if exit := streamQuestionHistoryHuman(t.Context(), sub, &stdout, &stderr); exit != 1 {
		t.Fatalf("exit = %d, want 1", exit)
	}
	if stdout.Len() == 0 || stderr.Len() == 0 {
		t.Fatalf("partial output lengths = stdout %d stderr %d", stdout.Len(), stderr.Len())
	}
}

func TestQuestionHistoryHumanStreamingReportsOutputFailure(t *testing.T) {
	sub := &questionHistoryScriptSubscription{
		events: []serverapi.QuestionHistoryEvent{
			{Kind: serverapi.QuestionHistoryEventStarted, LargeHistory: boolPointer(false)},
			{
				Kind: serverapi.QuestionHistoryEventQuestion,
				Question: &serverapi.QuestionHistoryQuestion{
					Question: "opaque-question",
					Answer:   "opaque-answer",
				},
			},
		},
	}
	var stderr bytes.Buffer
	if exit := streamQuestionHistoryHuman(t.Context(), sub, failingCLIWriter{}, &stderr); exit != 1 {
		t.Fatalf("exit = %d, want 1", exit)
	}
	if stderr.Len() == 0 {
		t.Fatal("output failure was not reported")
	}
}

func TestQuestionHistoryJSONStreamsExplicitNullsAndCompletion(t *testing.T) {
	sub := &questionHistoryScriptSubscription{
		events: []serverapi.QuestionHistoryEvent{
			{Kind: serverapi.QuestionHistoryEventStarted, LargeHistory: boolPointer(true)},
			{
				Kind: serverapi.QuestionHistoryEventQuestion,
				Question: &serverapi.QuestionHistoryQuestion{
					Question: "q",
					Answer:   "a",
				},
			},
			{Kind: serverapi.QuestionHistoryEventCompleted, HistoryOmitted: boolPointer(false)},
		},
	}
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if exit := streamQuestionHistoryJSON(t.Context(), sub, &stdout, &stderr); exit != 0 {
		t.Fatalf("exit = %d stderr=%s", exit, stderr.String())
	}
	var decoded struct {
		Questions []struct {
			SelectedOptionNumber *int    `json:"selected_option_number"`
			Commentary           *string `json:"commentary"`
			At                   *string `json:"at"`
		} `json:"questions"`
		HistoryOmitted bool `json:"history_omitted"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &decoded); err != nil {
		t.Fatalf("decode JSON output: %v", err)
	}
	if len(decoded.Questions) != 1 ||
		decoded.Questions[0].SelectedOptionNumber != nil ||
		decoded.Questions[0].Commentary != nil ||
		decoded.Questions[0].At != nil ||
		decoded.HistoryOmitted {
		t.Fatalf("decoded JSON output = %#v", decoded)
	}
}

func TestQuestionHistoryJSONLeavesPartialDocumentOnFailure(t *testing.T) {
	sub := &questionHistoryScriptSubscription{
		events: []serverapi.QuestionHistoryEvent{
			{Kind: serverapi.QuestionHistoryEventStarted, LargeHistory: boolPointer(false)},
			{
				Kind: serverapi.QuestionHistoryEventQuestion,
				Question: &serverapi.QuestionHistoryQuestion{
					Question: "q",
					Answer:   "a",
				},
			},
		},
		err: errors.New("terminal failure"),
	}
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if exit := streamQuestionHistoryJSON(t.Context(), sub, &stdout, &stderr); exit != 1 {
		t.Fatalf("exit = %d, want 1", exit)
	}
	if json.Valid(stdout.Bytes()) {
		t.Fatalf("partial failure output unexpectedly valid JSON: %s", stdout.Bytes())
	}
}

func TestQuestionHistoryListDispatchesAliasesAndSelectorContracts(t *testing.T) {
	unsetSessionIDEnvironmentForTest(t)
	explicit := uuid.NewString()
	invoking := uuid.NewString()
	if err := os.Setenv(sessionenv.SessionIDEnv, invoking); err != nil {
		t.Fatalf("set invoking Session: %v", err)
	}
	t.Cleanup(func() { _ = os.Unsetenv(sessionenv.SessionIDEnv) })
	for _, commandArgs := range [][]string{
		{"list", "--session", explicit, "--max-handoffs", "7"},
		{"history", "--session", explicit, "--max-handoffs", "7"},
	} {
		remote := &stubQuestionCommandRemote{}
		command, opened := questionCommandWithRemote(remote)
		if exit := command.run(commandArgs, io.Discard, io.Discard); exit != 0 {
			t.Fatalf("command %v exit = %d", commandArgs, exit)
		}
		if len(*opened) != 1 || (*opened)[0] != explicit {
			t.Fatalf("command %v opened Sessions = %v", commandArgs, *opened)
		}
		if len(remote.historyRequests) != 1 ||
			remote.historyRequests[0].SessionID != explicit ||
			remote.historyRequests[0].MaxHandoffs != 7 {
			t.Fatalf("command %v request = %+v", commandArgs, remote.historyRequests)
		}
	}

	remote := &stubQuestionCommandRemote{}
	command, opened := questionCommandWithRemote(remote)
	if exit := command.run([]string{"list"}, io.Discard, io.Discard); exit != 0 {
		t.Fatalf("invoking-Session list exit = %d", exit)
	}
	if len(*opened) != 1 || (*opened)[0] != invoking ||
		len(remote.historyRequests) != 1 ||
		remote.historyRequests[0].MaxHandoffs != 25 {
		t.Fatalf("invoking-Session selection opened=%v requests=%+v", *opened, remote.historyRequests)
	}
}

func TestQuestionHistoryListRejectsUnsupportedSelectorsAndBounds(t *testing.T) {
	sessionID := uuid.NewString()
	for _, args := range [][]string{
		{"list", "--session", sessionID, "--max-handoffs", "0"},
		{"list", "--session", sessionID, "--task", "KENT-583"},
	} {
		command, _ := questionCommandWithRemote(&stubQuestionCommandRemote{})
		if exit := command.run(args, io.Discard, io.Discard); exit != 2 {
			t.Fatalf("args %v exit = %d, want 2", args, exit)
		}
	}
}

func TestRunQuestionsTranslatesPositionalSessionToCanonicalList(t *testing.T) {
	sessionID := uuid.NewString()
	remote := &stubQuestionCommandRemote{}
	if exit := runQuestionsSubcommand(
		[]string{"--max-handoffs", "4", "--json", sessionID},
		io.Discard,
		io.Discard,
		func(context.Context, string) (questionCommandRemote, error) { return remote, nil },
	); exit != 0 {
		t.Fatalf("run questions exit = %d", exit)
	}
	if len(remote.historyRequests) != 1 ||
		remote.historyRequests[0].SessionID != sessionID ||
		remote.historyRequests[0].MaxHandoffs != 4 {
		t.Fatalf("run questions request = %+v", remote.historyRequests)
	}
}

func TestQuestionHistoryStreamingInterruptionAndJSONWriteFailure(t *testing.T) {
	canceled := &questionHistoryScriptSubscription{err: context.Canceled}
	if exit := streamQuestionHistoryHuman(t.Context(), canceled, io.Discard, io.Discard); exit != 130 {
		t.Fatalf("human interruption exit = %d", exit)
	}
	canceled = &questionHistoryScriptSubscription{err: context.Canceled}
	if exit := streamQuestionHistoryJSON(t.Context(), canceled, io.Discard, io.Discard); exit != 130 {
		t.Fatalf("JSON interruption exit = %d", exit)
	}
	sub := &questionHistoryScriptSubscription{}
	var stderr bytes.Buffer
	if exit := streamQuestionHistoryJSON(t.Context(), sub, failingCLIWriter{}, &stderr); exit != 1 || stderr.Len() == 0 {
		t.Fatalf("JSON write failure exit=%d stderr bytes=%d", exit, stderr.Len())
	}
}

func TestQuestionHistoryJSONConvertsAnswerTimeToUTC(t *testing.T) {
	at := transcript.CommittedAtUnixMs(time.Date(2026, time.August, 13, 12, 30, 0, 0, time.FixedZone("fixture", 2*60*60)).UnixMilli())
	sub := &questionHistoryScriptSubscription{
		events: []serverapi.QuestionHistoryEvent{
			{Kind: serverapi.QuestionHistoryEventStarted, LargeHistory: boolPointer(false)},
			{Kind: serverapi.QuestionHistoryEventQuestion, Question: &serverapi.QuestionHistoryQuestion{
				Question: "q", Answer: "a", At: &at,
			}},
			{Kind: serverapi.QuestionHistoryEventCompleted, HistoryOmitted: boolPointer(false)},
		},
	}
	var stdout bytes.Buffer
	if exit := streamQuestionHistoryJSON(t.Context(), sub, &stdout, io.Discard); exit != 0 {
		t.Fatalf("exit = %d", exit)
	}
	var decoded struct {
		Questions []struct {
			At time.Time `json:"at"`
		} `json:"questions"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &decoded); err != nil {
		t.Fatalf("decode JSON output: %v", err)
	}
	if len(decoded.Questions) != 1 ||
		decoded.Questions[0].At.Location() != time.UTC ||
		decoded.Questions[0].At.UnixMilli() != at.UnixMs() {
		t.Fatalf("decoded answer time = %+v", decoded.Questions)
	}
}

func boolPointer(value bool) *bool {
	return &value
}
