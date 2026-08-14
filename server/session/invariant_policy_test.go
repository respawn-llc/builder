package session

import (
	"testing"
)

func TestSessionInvariantFailuresReturnDiagnosticsInReleaseMode(t *testing.T) {
	t.Setenv("KENT_INVARIANT_MODE", "diagnostic")

	var record EventRecord
	if _, err := record.Kind(); err == nil {
		t.Fatal("zero record kind did not return an error")
	}
	if _, err := record.Payload(); err == nil {
		t.Fatal("zero record payload did not return an error")
	}
	if _, err := marshalSessionJSON(make(chan struct{})); err == nil {
		t.Fatal("unrepresentable JSON did not return an error")
	}

	var capability MaterializedEventLog
	if _, err := capability.Revision(); err == nil {
		t.Fatal("invalid capability revision did not return an error")
	}
	if _, err := capability.ConversationFreshness(); err == nil {
		t.Fatal("invalid capability freshness did not return an error")
	}
	if _, err := capability.ReadRecentRecords(1); err == nil {
		t.Fatal("invalid capability read did not return an error")
	}

}

func TestSessionInvariantFailuresPanicInDebugMode(t *testing.T) {
	t.Setenv("KENT_INVARIANT_MODE", "panic")

	defer func() {
		if recovered := recover(); recovered == nil {
			t.Fatal("debug invariant failure did not panic")
		}
	}()
	_, _ = EventRecord{}.Kind()
}
