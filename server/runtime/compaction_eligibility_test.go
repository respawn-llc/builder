package runtime

import (
	"testing"

	"core/server/llm"
	"core/server/session"
	"core/server/tools"
	"core/shared/sessioncontract"
	"core/shared/textutil"
)

func TestManualCompactionEligibilityRestoresFromTheNewestActiveSegment(t *testing.T) {
	t.Run("legacy and fresh sessions are ineligible", func(t *testing.T) {
		store := mustCreateTestSession(t)
		engine := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(), Config{})
		if engine.compactionRuntimeState().ManualCompactionEligible() {
			t.Fatal("fresh session is eligible before a boundary")
		}
	})

	t.Run("matching boundary in active segment is eligible", func(t *testing.T) {
		store := mustCreateTestSession(t)
		appendAgentStepBoundaryForEligibilityTest(t, store, "step-1")
		engine := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(), Config{})
		if !engine.compactionRuntimeState().ManualCompactionEligible() {
			t.Fatal("matching active-segment boundary did not restore eligibility")
		}
	})

	t.Run("successful replacement without a later boundary is too soon", func(t *testing.T) {
		store := mustCreateTestSession(t)
		appendAgentStepBoundaryForEligibilityTest(t, store, "step-1")
		log := mustMaterializeTestEventLog(t, store)
		stepID := "compact-1"
		if _, receipt, err := log.AppendCompactionHistoryReplacement(&stepID, session.HistoryReplacementRecord{
			Engine: "local",
			Mode:   session.CompactionModeManual,
		}); err != nil || !receipt.Committed {
			t.Fatalf("append replacement: receipt=%+v err=%v", receipt, err)
		}
		engine := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(), Config{})
		if engine.compactionRuntimeState().ManualCompactionEligible() {
			t.Fatal("replacement without later boundary remained eligible")
		}
	})
}

func TestManualCompactionEligibilityIgnoresCopiedParentBoundariesInForkAndClone(t *testing.T) {
	parent := mustCreateTestSession(t)
	appendAgentStepBoundaryForEligibilityTest(t, parent, "parent-step")
	parentLog := mustMaterializeTestEventLog(t, parent)
	user := "fork anchor"
	userRecord, _, err := parentLog.AppendRecord(nil, session.MessageRecord{
		Role:    session.MessageRoleUser,
		Content: &user,
	})
	if err != nil {
		t.Fatalf("append fork anchor: %v", err)
	}

	forked, _, err := session.ForkAtUserMessage(
		parentLog,
		userRecord.Seq(),
		"fork",
		sessioncontract.SessionCategoryMain,
	)
	if err != nil {
		t.Fatalf("fork session: %v", err)
	}
	cloned, err := session.CloneSession(parentLog, "clone", sessioncontract.SessionCategoryMain)
	if err != nil {
		t.Fatalf("clone session: %v", err)
	}

	for _, tc := range []struct {
		name  string
		store *session.Store
	}{
		{name: "fork", store: forked},
		{name: "clone", store: cloned},
	} {
		t.Run(tc.name, func(t *testing.T) {
			engine := mustNewTestEngine(t, tc.store, &fakeClient{}, tools.NewRegistry(), Config{})
			if engine.compactionRuntimeState().ManualCompactionEligible() {
				t.Fatalf("%s copied parent boundary authorized child", tc.name)
			}

			appendAgentStepBoundaryForEligibilityTest(t, tc.store, tc.name+"-step")
			reopened := mustNewTestEngine(t, tc.store, &fakeClient{}, tools.NewRegistry(), Config{})
			if !reopened.compactionRuntimeState().ManualCompactionEligible() {
				t.Fatalf("%s child-origin boundary did not authorize child", tc.name)
			}
		})
	}
}

func appendAgentStepBoundaryForEligibilityTest(t *testing.T, store *session.Store, stepID string) {
	t.Helper()
	log := mustMaterializeTestEventLog(t, store)
	if _, receipt, err := log.AppendAgentStepFinalization(stepID, []session.EventRecordPayload{
		session.MessageRecord{
			Role:    session.MessageRoleAssistant,
			Content: textutil.Value("completed step"),
			Phase:   textutil.Value(session.MessagePhaseFinal),
		},
	}); err != nil || !receipt.Committed {
		t.Fatalf("append boundary %q: receipt=%+v err=%v", stepID, receipt, err)
	}
}

var _ llm.Client = (*fakeClient)(nil)
