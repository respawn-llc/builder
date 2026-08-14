package core

import (
	"errors"
	"testing"

	"core/server/metadata"
)

func TestMetadataFatalAuthorityRetainsAndSignalsOnlyFirstCriticalFailure(t *testing.T) {
	authority := NewMetadataFatalAuthority()
	firstCause := errors.New("database full")
	first := &metadata.ClassifiedFailure{
		Class:     metadata.FailureCritical,
		Operation: "first operation",
		Cause:     firstCause,
	}
	second := &metadata.ClassifiedFailure{
		Class:     metadata.FailureCritical,
		Operation: "second operation",
		Cause:     errors.New("database corrupt"),
	}

	if !authority.ReportMetadataFatal(first) {
		t.Fatal("first critical failure was not accepted")
	}
	select {
	case <-authority.Done():
	default:
		t.Fatal("first critical failure did not signal supervision")
	}
	if authority.ReportMetadataFatal(second) {
		t.Fatal("second critical failure was accepted")
	}
	if got := authority.MetadataFatal(); got != first || !errors.Is(got, firstCause) {
		t.Fatalf("retained fatal = %#v, want first failure", got)
	}
	if authority.ReportMetadataFatal(&metadata.ClassifiedFailure{
		Class: metadata.FailureNoncritical,
		Cause: errors.New("busy"),
	}) {
		t.Fatal("noncritical failure was accepted")
	}
}
