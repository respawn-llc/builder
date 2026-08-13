package migrationcheck

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"

	"core/shared/runtimeids"
	"core/shared/serverapi"
)

func TestKENT345ProjectionOmitsOnlyGenericRequestIdentities(t *testing.T) {
	fixture := kent345ProjectionFixture(t)

	if err := CheckKENT345FiniteProjection(
		fixture.Legacy,
		fixture.Descriptor,
		KENT345ProjectionIdentities(),
	); err != nil {
		t.Fatal(err)
	}
}

func TestKENT345ProjectionRejectsAnyExtraOmission(t *testing.T) {
	fixture := kent345ProjectionFixture(t)

	for _, retained := range retainedKENT345IdentityFixtures() {
		t.Run(retained.Identity.String(), func(t *testing.T) {
			descriptor := removeFixtureIdentity(fixture.Descriptor, retained.Identity)
			assertProjectionIssue(
				t,
				CheckKENT345FiniteProjection(
					fixture.Legacy,
					descriptor,
					KENT345ProjectionIdentities(),
				),
				IssueUnapprovedProjection,
			)
		})
	}
}

func TestKENT345ProjectionExpansionRequiresPlanning(t *testing.T) {
	fixture := kent345ProjectionFixture(t)
	expanded := append(
		KENT345ProjectionIdentities(),
		fieldIdentity("fixture/envelope", "CallFrame", "CorrelationID"),
	)

	assertProjectionIssue(
		t,
		CheckKENT345FiniteProjection(fixture.Legacy, fixture.Descriptor, expanded),
		IssueUnexpectedProjectionIdentity,
	)
}

func TestKENT345ProjectedIdentitiesNeverAppearInDescriptorFixture(t *testing.T) {
	fixture := kent345ProjectionFixture(t)
	withProjectedField := append(
		append([]Identity(nil), fixture.Descriptor...),
		fieldIdentity("core/shared/serverapi", "RuntimeLiveSteerResponse", "ClientRequestID"),
	)

	assertProjectionIssue(
		t,
		CheckKENT345FiniteProjection(
			fixture.Legacy,
			withProjectedField,
			KENT345ProjectionIdentities(),
		),
		IssueProjectedIdentityAuthored,
	)
}

func TestKENT345PostProjectionStrictJSONRejectsGenericRequestIdentity(t *testing.T) {
	assertFocusedProjectionFixture(t, FocusedKENT345StrictJSON)
	validRunPrompt := []byte(`{"session_id":"session-1","prompt":"continue"}`)
	var runPrompt postKENT345RunPromptRequest
	if err := json.Unmarshal(validRunPrompt, &runPrompt); err != nil {
		t.Fatalf("decode post-projection run prompt: %v", err)
	}
	if err := runPrompt.Validate(); err != nil {
		t.Fatalf("validate post-projection run prompt: %v", err)
	}

	validSessionPlan := []byte(`{"mode":"headless","session_id":"session-1"}`)
	var sessionPlan postKENT345SessionPlanRequest
	if err := json.Unmarshal(validSessionPlan, &sessionPlan); err != nil {
		t.Fatalf("decode post-projection session plan: %v", err)
	}
	if err := sessionPlan.Validate(); err != nil {
		t.Fatalf("validate post-projection session plan: %v", err)
	}

	tests := []struct {
		raw    string
		target func() any
	}{
		{
			raw:    `{"client_request_id":"request-1","session_id":"session-1","prompt":"continue"}`,
			target: func() any { return &postKENT345RunPromptRequest{} },
		},
		{
			raw:    `{"session_id":"session-1","prompt":"continue","request_id":"similar-but-not-authorized"}`,
			target: func() any { return &postKENT345RunPromptRequest{} },
		},
		{
			raw:    `{"client_request_id":"request-1","mode":"headless","session_id":"session-1"}`,
			target: func() any { return &postKENT345SessionPlanRequest{} },
		},
	}
	for _, test := range tests {
		if err := json.Unmarshal([]byte(test.raw), test.target()); err == nil {
			t.Errorf("strict post-projection decode accepted %s", test.raw)
		}
	}
}

func TestKENT345RetainedNamedIdentityJSONAndValidationBehavior(t *testing.T) {
	assertFocusedProjectionFixture(t, FocusedKENT345CustomWire)
	const (
		queueItemRaw       = "11111111-1111-4111-8111-111111111111"
		setupOperationRaw  = "22222222-2222-4222-8222-222222222222"
		runRaw             = "33333333-3333-4333-8333-333333333333"
		agentStepRaw       = "44444444-4444-4444-8444-444444444444"
		sessionRaw         = "55555555-5555-4555-8555-555555555555"
		envelopeRequestRaw = "66666666-6666-4666-8666-666666666666"
	)

	queueItemID, err := runtimeids.ParseQueueItemID(queueItemRaw)
	if err != nil {
		t.Fatal(err)
	}
	runID, err := runtimeids.ParseRunID(runRaw)
	if err != nil {
		t.Fatal(err)
	}
	agentStepID, err := runtimeids.ParseStepID(agentStepRaw)
	if err != nil {
		t.Fatal(err)
	}
	sessionID, err := runtimeids.ParseSessionID(sessionRaw)
	if err != nil {
		t.Fatal(err)
	}
	setupOperationID, err := serverapi.ParseWorktreeSetupOperationID(setupOperationRaw)
	if err != nil {
		t.Fatal(err)
	}
	resourceGeneration := runtimeids.ResourceGeneration(1)
	if err := resourceGeneration.Validate(); err != nil {
		t.Fatalf("validate retained Resource Generation: %v", err)
	}
	if err := (runtimeids.ResourceGeneration(0)).Validate(); err == nil {
		t.Fatal("zero Resource Generation validated")
	}

	for name, value := range map[string]any{
		"Queue Item":      queueItemID,
		"Setup Operation": setupOperationID,
		"Run":             runID,
		"Agent Step":      agentStepID,
		"Session":         sessionID,
	} {
		encoded, err := json.Marshal(value)
		if err != nil {
			t.Errorf("marshal retained %s identity: %v", name, err)
			continue
		}
		if string(encoded) == `""` || string(encoded) == "null" {
			t.Errorf("retained %s identity encoded as absence: %s", name, encoded)
		}
	}

	call := postKENT345EnvelopeCall{
		CorrelationID: envelopeRequestRaw,
		Payload:       []byte{1},
	}
	if err := call.Validate(); err != nil {
		t.Fatalf("validate retained envelope correlation: %v", err)
	}
	if err := (postKENT345EnvelopeCall{Payload: []byte{1}}).Validate(); err == nil {
		t.Fatal("multiplexed envelope call accepted absent correlation")
	}
	if err := (postKENT345EnvelopeCall{CorrelationID: envelopeRequestRaw}).Validate(); err == nil {
		t.Fatal("multiplexed envelope call accepted absent descriptor-typed payload")
	}
}

type postKENT345EnvelopeCall struct {
	CorrelationID string
	Payload       []byte
}

func (c postKENT345EnvelopeCall) Validate() error {
	if err := runtimeids.ValidateUUIDv4(c.CorrelationID, "correlation_id"); err != nil {
		return err
	}
	if len(c.Payload) == 0 {
		return errors.New("descriptor-typed payload is required")
	}
	return nil
}

func TestKENT345PostProjectionHydrationUsesQueueItemIdentityOnly(t *testing.T) {
	assertFocusedProjectionFixture(t, FocusedKENT345Hydration)
	assertFocusedProjectionFixture(t, FocusedKENT345Uniqueness)
	first := postKENT345QueuedMessageState{
		QueueItemID: "queue-item-1",
		Status:      "accepted",
		Text:        "first",
	}
	second := postKENT345QueuedMessageState{
		QueueItemID: "queue-item-2",
		Status:      "accepted",
		Text:        "second",
	}
	if err := validatePostKENT345Hydration([]postKENT345QueuedMessageState{first, second}); err != nil {
		t.Fatalf("validate post-projection hydration: %v", err)
	}
	if err := validatePostKENT345Hydration([]postKENT345QueuedMessageState{first, first}); err == nil {
		t.Fatal("post-projection hydration accepted duplicate Queue Item identity")
	}
	if err := (postKENT345QueuedMessageState{Status: "accepted", Text: "missing id"}).Validate(); err == nil {
		t.Fatal("post-projection queued-message state accepted an absent Queue Item identity")
	}
}

func TestKENT345PostProjectionTranscriptEventRetainsAgentStepIdentity(t *testing.T) {
	assertFocusedProjectionFixture(t, FocusedKENT345CustomWire)
	if err := (postKENT345UserMessageFlushed{AgentStepID: "agent-step-1"}).Validate(); err != nil {
		t.Fatalf("validate post-projection transcript event: %v", err)
	}
	if err := (postKENT345UserMessageFlushed{}).Validate(); err == nil {
		t.Fatal("post-projection transcript event accepted an absent Agent Step identity")
	}
}

func TestKENT345PostProjectionMixedValidatorsPreserveRetainedBehavior(t *testing.T) {
	assertFocusedProjectionFixture(t, FocusedKENT345MixedValidators)
	valid := []postKENT345Validator{
		postKENT345RunPromptRequest{SessionID: "session-1", Prompt: "continue"},
		postKENT345SessionPlanRequest{Mode: "headless", SessionID: "session-1"},
		postKENT345RuntimeSubmitRequest{SessionID: "session-1", Input: "continue"},
		postKENT345LiveSteerResponse{QueueItemID: "queue-item-1", Text: "continue"},
	}
	for _, value := range valid {
		if err := value.Validate(); err != nil {
			t.Errorf("%T rejected retained behavior: %v", value, err)
		}
	}

	invalid := []postKENT345Validator{
		postKENT345RunPromptRequest{SessionID: "session-1"},
		postKENT345SessionPlanRequest{Mode: "unknown", SessionID: "session-1"},
		postKENT345RuntimeSubmitRequest{Input: "continue"},
		postKENT345LiveSteerResponse{Text: "continue"},
	}
	for _, value := range invalid {
		if err := value.Validate(); err == nil {
			t.Errorf("%T accepted invalid retained behavior", value)
		}
	}
}

type kent345RetainedIdentityFixture struct {
	Name     string
	Identity Identity
}

func retainedKENT345IdentityFixtures() []kent345RetainedIdentityFixture {
	return []kent345RetainedIdentityFixture{
		{Name: "Queue Item", Identity: typeIdentity("core/shared/runtimeids", "QueueItemID")},
		{Name: "Setup Operation", Identity: typeIdentity("core/shared/serverapi", "WorktreeSetupOperationID")},
		{Name: "Run", Identity: typeIdentity("core/shared/runtimeids", "RunID")},
		{Name: "Agent Step", Identity: typeIdentity("core/shared/runtimeids", "StepID")},
		{Name: "Session", Identity: typeIdentity("core/shared/runtimeids", "SessionID")},
		{Name: "Resource Generation", Identity: typeIdentity("core/shared/runtimeids", "ResourceGeneration")},
		{Name: "envelope correlation", Identity: fieldIdentity("fixture/envelope", "CallFrame", "CorrelationID")},
	}
}

type kent345Fixture struct {
	Legacy     []Identity
	Descriptor []Identity
}

func kent345ProjectionFixture(t *testing.T) kent345Fixture {
	t.Helper()
	retained := retainedKENT345IdentityFixtures()
	retainedIdentities := make([]Identity, 0, len(retained))
	for _, fixture := range retained {
		retainedIdentities = append(retainedIdentities, fixture.Identity)
	}
	legacy := append([]Identity(nil), KENT345ProjectionIdentities()...)
	legacy = append(legacy, retainedIdentities...)
	return kent345Fixture{
		Legacy:     legacy,
		Descriptor: retainedIdentities,
	}
}

type postKENT345Validator interface {
	Validate() error
}

type postKENT345RunPromptRequest struct {
	SessionID string `json:"session_id"`
	Prompt    string `json:"prompt"`
}

func (r *postKENT345RunPromptRequest) UnmarshalJSON(data []byte) error {
	type wire postKENT345RunPromptRequest
	var decoded wire
	if err := decodeStrictJSON(data, &decoded); err != nil {
		return err
	}
	*r = postKENT345RunPromptRequest(decoded)
	return nil
}

func (r postKENT345RunPromptRequest) Validate() error {
	if strings.TrimSpace(r.SessionID) == "" {
		return errors.New("Session identity is required")
	}
	if strings.TrimSpace(r.Prompt) == "" {
		return errors.New("prompt is required")
	}
	return nil
}

type postKENT345SessionPlanRequest struct {
	Mode      string `json:"mode"`
	SessionID string `json:"session_id"`
}

func (r *postKENT345SessionPlanRequest) UnmarshalJSON(data []byte) error {
	type wire postKENT345SessionPlanRequest
	var decoded wire
	if err := decodeStrictJSON(data, &decoded); err != nil {
		return err
	}
	*r = postKENT345SessionPlanRequest(decoded)
	return nil
}

func (r postKENT345SessionPlanRequest) Validate() error {
	if r.Mode != "headless" && r.Mode != "interactive" {
		return errors.New("unknown session mode")
	}
	if strings.TrimSpace(r.SessionID) == "" {
		return errors.New("Session identity is required")
	}
	return nil
}

type postKENT345RuntimeSubmitRequest struct {
	SessionID string
	Input     string
}

func (r postKENT345RuntimeSubmitRequest) Validate() error {
	if strings.TrimSpace(r.SessionID) == "" {
		return errors.New("Session identity is required")
	}
	if strings.TrimSpace(r.Input) == "" {
		return errors.New("runtime input is required")
	}
	return nil
}

type postKENT345LiveSteerResponse struct {
	QueueItemID string
	Text        string
}

func (r postKENT345LiveSteerResponse) Validate() error {
	if strings.TrimSpace(r.QueueItemID) == "" {
		return errors.New("Queue Item identity is required")
	}
	if strings.TrimSpace(r.Text) == "" {
		return errors.New("queued text is required")
	}
	return nil
}

type postKENT345QueuedMessageState struct {
	QueueItemID string
	Status      string
	Text        string
}

func (s postKENT345QueuedMessageState) Validate() error {
	if strings.TrimSpace(s.QueueItemID) == "" {
		return errors.New("Queue Item identity is required")
	}
	if s.Status != "accepted" {
		return errors.New("hydrated Queue Item must be accepted")
	}
	if strings.TrimSpace(s.Text) == "" {
		return errors.New("queued text is required")
	}
	return nil
}

func validatePostKENT345Hydration(messages []postKENT345QueuedMessageState) error {
	seenQueueItems := make(map[string]struct{}, len(messages))
	for index, message := range messages {
		if err := message.Validate(); err != nil {
			return fmt.Errorf("validate hydrated Queue Item %d: %w", index, err)
		}
		if _, exists := seenQueueItems[message.QueueItemID]; exists {
			return fmt.Errorf("hydration repeats Queue Item identity %q", message.QueueItemID)
		}
		seenQueueItems[message.QueueItemID] = struct{}{}
	}
	return nil
}

type postKENT345UserMessageFlushed struct {
	AgentStepID string
}

func (e postKENT345UserMessageFlushed) Validate() error {
	if strings.TrimSpace(e.AgentStepID) == "" {
		return errors.New("Agent Step identity is required")
	}
	return nil
}

func decodeStrictJSON(data []byte, target any) error {
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("multiple JSON values")
		}
		return err
	}
	return nil
}
