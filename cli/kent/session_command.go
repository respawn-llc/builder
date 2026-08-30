package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"core/shared/client"
	"core/shared/config"
	sessionlaunchpb "core/shared/protoapi/gen/kent/api/session_launch"
	sharedpb "core/shared/protoapi/gen/kent/api/shared"
	"core/shared/sessionenv"
)

const selfSessionRemovalMessage = "You're trying to delete your own session, which is effectively a suicide. Don't do it, you still have things worth living for! Seek help immediately via ask_question, or exclude your session if this is accidental"

type sessionCommandRemote interface {
	ArchiveSession(context.Context, *sessionlaunchpb.SessionArchiveRequest) (*sessionlaunchpb.SessionArchiveSuccess, error)
	DeleteSession(context.Context, *sessionlaunchpb.SessionDeleteRequest) (*sessionlaunchpb.SessionDeleteSuccess, error)
	Close() error
}

type sessionCommandRemoteOpener func(context.Context) (sessionCommandRemote, error)

type sessionCommand struct {
	openRemote sessionCommandRemoteOpener
}

type sessionRemovalOperation uint8

const (
	sessionArchiveOperation sessionRemovalOperation = iota + 1
	sessionDeleteOperation
)

type sessionRemovalResult struct {
	SessionID  string  `json:"session_id"`
	OutputPath *string `json:"output_path,omitempty"`
}

type sessionRemovalError struct {
	Code      string  `json:"code"`
	Message   string  `json:"message"`
	SessionID string  `json:"session_id"`
	Path      *string `json:"path,omitempty"`
}

type sessionRemovalOutcome struct {
	Result *sessionRemovalResult
	Error  *sessionRemovalError
}

type sessionRemovalJSONEnvelope struct {
	Status string                `json:"status"`
	Result *sessionRemovalResult `json:"result,omitempty"`
	Error  *sessionRemovalError  `json:"error,omitempty"`
}

func sessionSubcommand(args []string, stdout io.Writer, stderr io.Writer) int {
	return sessionCommand{openRemote: openSessionCommandRemote}.run(args, stdout, stderr)
}

func (c sessionCommand) run(args []string, stdout io.Writer, stderr io.Writer) int {
	return dispatchCommandGroup(args, stdout, stderr, commandGroup{
		path:  "session",
		usage: sessionUsage,
		routes: map[string]commandHandler{
			"archive": c.archive,
			"delete":  c.delete,
		},
	})
}

func (c sessionCommand) archive(args []string, stdout io.Writer, stderr io.Writer) int {
	fs := newCommandFlagSet(config.Command+" session archive", stderr, sessionArchiveUsage)
	outputPath := fs.String("output", "", "absolute server-local .tar.zst output path")
	jsonOut := fs.Bool("json", false, "write a stable JSON envelope")
	positionals, ok, exitCode := parseInterspersedPositionals(fs, args)
	if !ok {
		return exitCode
	}
	if len(positionals) != 1 {
		fmt.Fprintln(stderr, "session archive requires <session-id>")
		return 2
	}
	sessionID := strings.TrimSpace(positionals[0])
	if sessionID == "" {
		fmt.Fprintln(stderr, "Session ID must not be blank")
		return 2
	}
	if strings.TrimSpace(*outputPath) == "" {
		fmt.Fprintln(stderr, "session archive requires --output <path>")
		return 2
	}
	if sessionCommandTargetsSelf(sessionID) {
		return writeSessionRemovalOutcome(stdout, stderr, sessionRemovalFailureOutcome(
			sessionID,
			"self_session_forbidden",
			selfSessionRemovalMessage,
		), *jsonOut)
	}
	remote, err := c.dial()
	if err != nil {
		return writeSessionRemovalOutcome(
			stdout,
			stderr,
			sessionArchiveFailure(sessionID, *outputPath, err),
			*jsonOut,
		)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	outcome := runSessionArchiveUseCase(ctx, remote, sessionID, *outputPath)
	stop()
	exitCode = writeSessionRemovalOutcome(stdout, stderr, outcome, *jsonOut)
	if closeErr := remote.Close(); closeErr != nil {
		fmt.Fprintf(stderr, "close Session archive remote: %v\n", closeErr)
	}
	return exitCode
}

func (c sessionCommand) delete(args []string, stdout io.Writer, stderr io.Writer) int {
	fs := newCommandFlagSet(config.Command+" session delete", stderr, sessionDeleteUsage)
	confirmed := fs.Bool("confirm", false, "confirm Session deletion")
	jsonOut := fs.Bool("json", false, "write a stable JSON envelope")
	positionals, ok, exitCode := parseInterspersedPositionals(fs, args)
	if !ok {
		return exitCode
	}
	if len(positionals) != 1 {
		fmt.Fprintln(stderr, "session delete requires <session-id>")
		return 2
	}
	sessionID := strings.TrimSpace(positionals[0])
	if sessionID == "" {
		fmt.Fprintln(stderr, "Session ID must not be blank")
		return 2
	}
	if sessionCommandTargetsSelf(sessionID) {
		return writeSessionRemovalOutcome(stdout, stderr, sessionRemovalFailureOutcome(
			sessionID,
			"self_session_forbidden",
			selfSessionRemovalMessage,
		), *jsonOut)
	}
	if !*confirmed {
		return writeSessionRemovalOutcome(stdout, stderr, sessionRemovalFailureOutcome(
			sessionID,
			"confirmation_required",
			fmt.Sprintf(
				"Session deletion was not confirmed. Rerun with --confirm to delete session %s.",
				sessionID,
			),
		), *jsonOut)
	}
	remote, err := c.dial()
	if err != nil {
		return writeSessionRemovalOutcome(
			stdout,
			stderr,
			sessionDeleteFailure(sessionID, err),
			*jsonOut,
		)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	outcome := runSessionDeleteUseCase(ctx, remote, sessionID)
	stop()
	exitCode = writeSessionRemovalOutcome(stdout, stderr, outcome, *jsonOut)
	if closeErr := remote.Close(); closeErr != nil {
		fmt.Fprintf(stderr, "close Session deletion remote: %v\n", closeErr)
	}
	return exitCode
}

func (c sessionCommand) dial() (sessionCommandRemote, error) {
	if c.openRemote == nil {
		return nil, errors.New("Session command remote opener is required")
	}
	dialCtx, cancel := context.WithTimeout(context.Background(), bindingCommandRPCTimeout)
	defer cancel()
	return c.openRemote(dialCtx)
}

func openSessionCommandRemote(ctx context.Context) (sessionCommandRemote, error) {
	_, remote, err := openBindingCommandRemote(ctx, ".")
	return remote, err
}

func sessionCommandTargetsSelf(sessionID string) bool {
	current, ok := sessionenv.LookupSessionID(os.LookupEnv)
	return ok && current == sessionID
}

func runSessionArchiveUseCase(
	ctx context.Context,
	remote sessionCommandRemote,
	sessionID string,
	outputPath string,
) sessionRemovalOutcome {
	response, err := remote.ArchiveSession(ctx, &sessionlaunchpb.SessionArchiveRequest{
		SessionId:  sessionID,
		OutputPath: outputPath,
	})
	if err != nil {
		return sessionArchiveFailure(sessionID, outputPath, err)
	}
	if response == nil || response.SessionId != sessionID || response.OutputPath != outputPath {
		return sessionRemovalFailureOutcome(
			sessionID,
			"request_failed",
			"Session archive returned an invalid response.",
		)
	}
	return sessionRemovalOutcome{Result: &sessionRemovalResult{
		SessionID:  sessionID,
		OutputPath: &outputPath,
	}}
}

func runSessionDeleteUseCase(
	ctx context.Context,
	remote sessionCommandRemote,
	sessionID string,
) sessionRemovalOutcome {
	response, err := remote.DeleteSession(ctx, &sessionlaunchpb.SessionDeleteRequest{
		SessionId: sessionID,
	})
	if err != nil {
		return sessionDeleteFailure(sessionID, err)
	}
	if response == nil || response.SessionId != sessionID {
		return sessionRemovalFailureOutcome(
			sessionID,
			"request_failed",
			"Session deletion returned an invalid response.",
		)
	}
	return sessionRemovalOutcome{Result: &sessionRemovalResult{SessionID: sessionID}}
}

func sessionArchiveFailure(sessionID string, outputPath string, err error) sessionRemovalOutcome {
	var wire *client.SessionArchiveFailureError
	if !errors.As(err, &wire) || wire == nil || wire.Failure == nil {
		return sessionRemovalRequestFailure(sessionID, sessionArchiveOperation)
	}
	failure := wire.Failure
	if outcome, handled := commonSessionRemovalFailure(
		sessionID,
		sessionArchiveOperation,
		failure.Code,
		failure.GetSessionNotFound(),
		failure.GetSessionInUse(),
		failure.GetInternalFailure(),
	); handled {
		return outcome
	}
	switch failure.Code {
	case "invalid_output_path":
		if details := failure.GetInvalidOutputPath(); details != nil {
			return sessionRemovalPathFailureOutcome(
				sessionID,
				"invalid_output_path",
				invalidArchiveOutputPathMessage(details),
				details.Path,
			)
		}
	case "output_exists":
		if details := failure.GetOutputExists(); details != nil {
			return sessionRemovalPathFailureOutcome(
				sessionID,
				"output_exists",
				fmt.Sprintf("Archive output already exists at %s.", details.Path),
				details.Path,
			)
		}
	case "archive_path_failure":
		if details := failure.GetArchivePathFailure(); details != nil {
			return sessionRemovalPathFailureOutcome(
				sessionID,
				"request_failed",
				fmt.Sprintf(
					"Session archive failed while %s at %s.",
					archivePathFailureAction(details.Phase),
					details.Path,
				),
				details.Path,
			)
		}
	case "session_removal_failure":
		if details := failure.GetSessionRemovalFailure(); details != nil {
			return archiveRemovalFailureOutcome(sessionID, outputPath, details)
		}
	}
	return sessionRemovalUnsupportedFailure(sessionID, sessionArchiveOperation)
}

func sessionDeleteFailure(sessionID string, err error) sessionRemovalOutcome {
	var wire *client.SessionDeleteFailureError
	if !errors.As(err, &wire) || wire == nil || wire.Failure == nil {
		return sessionRemovalRequestFailure(sessionID, sessionDeleteOperation)
	}
	failure := wire.Failure
	if outcome, handled := commonSessionRemovalFailure(
		sessionID,
		sessionDeleteOperation,
		failure.Code,
		failure.GetSessionNotFound(),
		failure.GetSessionInUse(),
		failure.GetInternalFailure(),
	); handled {
		return outcome
	}
	switch failure.Code {
	case "session_removal_failure":
		if details := failure.GetSessionRemovalFailure(); details != nil {
			return deleteRemovalFailureOutcome(sessionID, details)
		}
	}
	return sessionRemovalUnsupportedFailure(sessionID, sessionDeleteOperation)
}

func commonSessionRemovalFailure(
	sessionID string,
	operation sessionRemovalOperation,
	code string,
	notFound *sessionlaunchpb.SessionNotFoundDetails,
	inUse *sessionlaunchpb.SessionInUseDetails,
	internal *sharedpb.InternalFailureDetails,
) (sessionRemovalOutcome, bool) {
	switch code {
	case "session_not_found":
		if notFound != nil {
			return sessionRemovalFailureOutcome(
				sessionID,
				"session_not_found",
				fmt.Sprintf("Session %s was not found.", sessionID),
			), true
		}
	case "session_in_use":
		if inUse != nil {
			return sessionRemovalFailureOutcome(
				sessionID,
				"session_in_use",
				fmt.Sprintf("Session %s is in use and cannot be removed.", sessionID),
			), true
		}
	case "internal_failure":
		if internal != nil {
			return sessionRemovalRequestFailure(sessionID, operation), true
		}
	}
	return sessionRemovalOutcome{}, false
}

func sessionRemovalRequestFailure(
	sessionID string,
	operation sessionRemovalOperation,
) sessionRemovalOutcome {
	return sessionRemovalFailureOutcome(
		sessionID,
		"request_failed",
		operation.requestFailureMessage(),
	)
}

func sessionRemovalUnsupportedFailure(
	sessionID string,
	operation sessionRemovalOperation,
) sessionRemovalOutcome {
	return sessionRemovalFailureOutcome(
		sessionID,
		"request_failed",
		operation.unsupportedFailureMessage(),
	)
}

func (o sessionRemovalOperation) requestFailureMessage() string {
	switch o {
	case sessionArchiveOperation:
		return "Session archive request failed."
	case sessionDeleteOperation:
		return "Session deletion request failed."
	default:
		return "Session removal request failed."
	}
}

func (o sessionRemovalOperation) unsupportedFailureMessage() string {
	switch o {
	case sessionArchiveOperation:
		return "Session archive returned an unsupported failure response."
	case sessionDeleteOperation:
		return "Session deletion returned an unsupported failure response."
	default:
		return "Session removal returned an unsupported failure response."
	}
}

func invalidArchiveOutputPathMessage(details *sessionlaunchpb.InvalidArchiveOutputPathDetails) string {
	switch details.Reason {
	case sessionlaunchpb.InvalidArchiveOutputPathReason_INVALID_ARCHIVE_OUTPUT_PATH_REASON_NOT_ABSOLUTE:
		return fmt.Sprintf("Archive output path %s must be absolute.", details.Path)
	case sessionlaunchpb.InvalidArchiveOutputPathReason_INVALID_ARCHIVE_OUTPUT_PATH_REASON_INVALID_SUFFIX:
		return fmt.Sprintf("Archive output path %s must end in .tar.zst.", details.Path)
	default:
		return fmt.Sprintf("Archive output path %s is invalid.", details.Path)
	}
}

func archivePathFailureAction(phase sessionlaunchpb.ArchivePathFailurePhase) string {
	switch phase {
	case sessionlaunchpb.ArchivePathFailurePhase_ARCHIVE_PATH_FAILURE_PHASE_PARENT:
		return "creating its parent directory"
	case sessionlaunchpb.ArchivePathFailurePhase_ARCHIVE_PATH_FAILURE_PHASE_TEMP:
		return "creating its temporary artifact"
	case sessionlaunchpb.ArchivePathFailurePhase_ARCHIVE_PATH_FAILURE_PHASE_WRITE:
		return "writing the artifact"
	case sessionlaunchpb.ArchivePathFailurePhase_ARCHIVE_PATH_FAILURE_PHASE_PUBLISH:
		return "publishing the artifact"
	case sessionlaunchpb.ArchivePathFailurePhase_ARCHIVE_PATH_FAILURE_PHASE_CLEANUP:
		return "cleaning its temporary artifact"
	default:
		return "processing the artifact"
	}
}

func archiveRemovalFailureOutcome(
	sessionID string,
	outputPath string,
	details *sessionlaunchpb.SessionRemovalFailureDetails,
) sessionRemovalOutcome {
	if details.GetMetadataNotRemoved() != nil {
		return sessionRemovalPathFailureOutcome(
			sessionID,
			"request_failed",
			fmt.Sprintf(
				"Archive %s exists, but Session %s was retained. Resolve the blocker, then run kent session delete %s --confirm.",
				outputPath,
				sessionID,
				sessionID,
			),
			outputPath,
		)
	}
	if cleanup := details.GetMetadataRemovedCleanupFailed(); cleanup != nil {
		return metadataRemovedCleanupFailureOutcome(sessionID, cleanup.RemainingPath)
	}
	return sessionRemovalFailureOutcome(
		sessionID,
		"request_failed",
		"Session archive returned an unsupported removal state.",
	)
}

func deleteRemovalFailureOutcome(
	sessionID string,
	details *sessionlaunchpb.SessionRemovalFailureDetails,
) sessionRemovalOutcome {
	if cleanup := details.GetMetadataRemovedCleanupFailed(); cleanup != nil {
		return metadataRemovedCleanupFailureOutcome(sessionID, cleanup.RemainingPath)
	}
	return sessionRemovalFailureOutcome(
		sessionID,
		"request_failed",
		"Session deletion failed before metadata removal.",
	)
}

func metadataRemovedCleanupFailureOutcome(
	sessionID string,
	remainingPath string,
) sessionRemovalOutcome {
	return sessionRemovalPathFailureOutcome(
		sessionID,
		"request_failed",
		fmt.Sprintf(
			"Session %s is gone from Kent, but artifact cleanup failed. Remove %s manually.",
			sessionID,
			remainingPath,
		),
		remainingPath,
	)
}

func sessionRemovalFailureOutcome(
	sessionID string,
	code string,
	message string,
) sessionRemovalOutcome {
	return sessionRemovalOutcome{Error: &sessionRemovalError{
		Code:      code,
		Message:   message,
		SessionID: sessionID,
	}}
}

func sessionRemovalPathFailureOutcome(
	sessionID string,
	code string,
	message string,
	path string,
) sessionRemovalOutcome {
	outcome := sessionRemovalFailureOutcome(sessionID, code, message)
	outcome.Error.Path = &path
	return outcome
}

func writeSessionRemovalOutcome(
	stdout io.Writer,
	stderr io.Writer,
	outcome sessionRemovalOutcome,
	jsonOut bool,
) int {
	if (outcome.Result == nil) == (outcome.Error == nil) {
		fmt.Fprintln(stderr, "Session removal returned an invalid outcome")
		return 1
	}
	if outcome.Error != nil {
		if jsonOut {
			if exitCode := writeCommandJSON(stdout, stderr, sessionRemovalJSONEnvelope{
				Status: "error",
				Error:  outcome.Error,
			}); exitCode != 0 {
				return exitCode
			}
			return 1
		}
		fmt.Fprintln(stderr, outcome.Error.Message)
		return 1
	}
	if jsonOut {
		return writeCommandJSON(stdout, stderr, sessionRemovalJSONEnvelope{
			Status: "ok",
			Result: outcome.Result,
		})
	}
	if _, err := fmt.Fprintln(stdout, "done"); err != nil {
		fmt.Fprintf(stderr, "write Session removal result: %v\n", err)
		return 1
	}
	return 0
}
