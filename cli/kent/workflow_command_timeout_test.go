package main

import (
	"testing"
	"time"
)

func TestWorkflowCommandTimeoutAllowsLongRunningTaskCleanup(t *testing.T) {
	if workflowCommandTimeout != time.Minute {
		t.Fatalf("workflow command timeout = %s, want %s", workflowCommandTimeout, time.Minute)
	}
}
