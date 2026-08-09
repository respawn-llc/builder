package serverattach

import (
	"context"
	"errors"
	"testing"
	"time"

	"core/shared/apicontract"
	"core/shared/serverapi"
)

func TestAttachRunPromptValidationFailuresCloseRemote(t *testing.T) {
	authErr := errors.New("auth failed")
	for _, tc := range []struct {
		name          string
		attachProject bool
		authErr       error
		wantErr       error
		notErr        error
	}{
		{name: "auth", attachProject: true, authErr: authErr, wantErr: authErr, notErr: serverapi.ErrWorkspaceNotRegistered},
		{name: "project", wantErr: serverapi.ErrWorkspaceNotRegistered},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dialWorkspace, closeServer, disconnected := dialWorkspaceServerWithRoot(t, "", tc.attachProject)
			defer closeServer()
			req := testAttachRequest(planProjectDial(boundPlanResponse()))
			req.DialWorkspace = dialWorkspace
			if tc.authErr != nil {
				req.EnsureAuthReady = func(context.Context, apicontract.AuthBootstrapService) error {
					return tc.authErr
				}
			}
			_, _, err := AttachRunPrompt(context.Background(), req)
			if !errors.Is(err, tc.wantErr) || tc.notErr != nil && errors.Is(err, tc.notErr) {
				t.Fatalf("AttachRunPrompt error = %v, want %v", err, tc.wantErr)
			}
			select {
			case <-disconnected:
			case <-time.After(time.Second):
				t.Fatal("validation failure did not close the attached remote")
			}
		})
	}
}

func TestRunPromptValidationFailureClosesExactlyOnceAndJoinsCloseError(t *testing.T) {
	validationErr := errors.New("validation failed")
	closeErr := errors.New("close failed")
	closeCalls := 0
	err := closeRunPromptValidationFailure(validationErr, func() error {
		closeCalls++
		return closeErr
	})
	if !errors.Is(err, validationErr) || !errors.Is(err, closeErr) {
		t.Fatalf("error = %v, want joined validation and close errors", err)
	}
	if closeCalls != 1 {
		t.Fatalf("close calls = %d, want 1", closeCalls)
	}
}
