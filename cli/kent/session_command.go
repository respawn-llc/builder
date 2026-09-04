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

type sessionRemovalResult struct {
	SessionID  string  `json:"session_id"`
	OutputPath *string `json:"output_path,omitempty"`
}

type sessionRemovalError struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	SessionID string `json:"session_id"`
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
		return writeSessionRemovalOutcome(stdout, stderr, nil, sessionRemovalFailure(
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
			nil,
			sessionArchiveFailure(sessionID, err),
			*jsonOut,
		)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	result, err := runSessionArchiveUseCase(ctx, remote, sessionID, *outputPath)
	stop()
	var failure *sessionRemovalError
	if err != nil {
		failure = sessionArchiveFailure(sessionID, err)
	}
	exitCode = writeSessionRemovalOutcome(stdout, stderr, result, failure, *jsonOut)
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
		return writeSessionRemovalOutcome(stdout, stderr, nil, sessionRemovalFailure(
			sessionID,
			"self_session_forbidden",
			selfSessionRemovalMessage,
		), *jsonOut)
	}
	if !*confirmed {
		return writeSessionRemovalOutcome(stdout, stderr, nil, sessionRemovalFailure(
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
			nil,
			sessionDeleteFailure(sessionID, err),
			*jsonOut,
		)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	result, err := runSessionDeleteUseCase(ctx, remote, sessionID)
	stop()
	var failure *sessionRemovalError
	if err != nil {
		failure = sessionDeleteFailure(sessionID, err)
	}
	exitCode = writeSessionRemovalOutcome(stdout, stderr, result, failure, *jsonOut)
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
) (*sessionRemovalResult, error) {
	response, err := remote.ArchiveSession(ctx, &sessionlaunchpb.SessionArchiveRequest{
		SessionId:  sessionID,
		OutputPath: outputPath,
	})
	if err != nil {
		return nil, err
	}
	return &sessionRemovalResult{
		SessionID:  response.SessionId,
		OutputPath: &response.OutputPath,
	}, nil
}

func runSessionDeleteUseCase(
	ctx context.Context,
	remote sessionCommandRemote,
	sessionID string,
) (*sessionRemovalResult, error) {
	response, err := remote.DeleteSession(ctx, &sessionlaunchpb.SessionDeleteRequest{
		SessionId: sessionID,
	})
	if err != nil {
		return nil, err
	}
	return &sessionRemovalResult{SessionID: response.SessionId}, nil
}

func sessionArchiveFailure(sessionID string, err error) *sessionRemovalError {
	var wire *client.SessionArchiveFailureError
	if !errors.As(err, &wire) || wire == nil || wire.Failure == nil {
		return sessionRemovalFailure(sessionID, "request_failed", err.Error())
	}
	failure := wire.Failure
	if expected := expectedSessionRemovalFailure(
		sessionID,
		failure.Code,
		failure.GetSessionNotFound(),
		failure.GetSessionInUse(),
	); expected != nil {
		return expected
	}
	switch failure.Code {
	case "invalid_output_path":
		if details := failure.GetInvalidOutputPath(); details != nil {
			return sessionRemovalFailure(
				sessionID,
				"invalid_output_path",
				invalidArchiveOutputPathMessage(details),
			)
		}
	case "output_exists":
		if details := failure.GetOutputExists(); details != nil {
			return sessionRemovalFailure(
				sessionID,
				"output_exists",
				fmt.Sprintf("Archive output already exists at %s.", details.Path),
			)
		}
	}
	return sessionRemovalFailure(sessionID, "request_failed", err.Error())
}

func sessionDeleteFailure(sessionID string, err error) *sessionRemovalError {
	var wire *client.SessionDeleteFailureError
	if !errors.As(err, &wire) || wire == nil || wire.Failure == nil {
		return sessionRemovalFailure(sessionID, "request_failed", err.Error())
	}
	failure := wire.Failure
	if expected := expectedSessionRemovalFailure(
		sessionID,
		failure.Code,
		failure.GetSessionNotFound(),
		failure.GetSessionInUse(),
	); expected != nil {
		return expected
	}
	return sessionRemovalFailure(sessionID, "request_failed", err.Error())
}

func expectedSessionRemovalFailure(
	sessionID string,
	code string,
	notFound *sessionlaunchpb.SessionNotFoundDetails,
	inUse *sessionlaunchpb.SessionInUseDetails,
) *sessionRemovalError {
	switch code {
	case "session_not_found":
		if notFound != nil {
			return sessionRemovalFailure(
				sessionID,
				"session_not_found",
				fmt.Sprintf("Session %s was not found.", sessionID),
			)
		}
	case "session_in_use":
		if inUse != nil {
			return sessionRemovalFailure(
				sessionID,
				"session_in_use",
				fmt.Sprintf("Session %s is in use and cannot be removed.", sessionID),
			)
		}
	}
	return nil
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

func sessionRemovalFailure(
	sessionID string,
	code string,
	message string,
) *sessionRemovalError {
	return &sessionRemovalError{
		Code:      code,
		Message:   message,
		SessionID: sessionID,
	}
}

func writeSessionRemovalOutcome(
	stdout io.Writer,
	stderr io.Writer,
	result *sessionRemovalResult,
	failure *sessionRemovalError,
	jsonOut bool,
) int {
	if failure != nil {
		if jsonOut {
			if exitCode := writeCommandJSON(stdout, stderr, sessionRemovalJSONEnvelope{
				Status: "error",
				Error:  failure,
			}); exitCode != 0 {
				return exitCode
			}
			return 1
		}
		fmt.Fprintln(stderr, failure.Message)
		return 1
	}
	if jsonOut {
		return writeCommandJSON(stdout, stderr, sessionRemovalJSONEnvelope{
			Status: "ok",
			Result: result,
		})
	}
	if _, err := fmt.Fprintln(stdout, "done"); err != nil {
		fmt.Fprintf(stderr, "write Session removal result: %v\n", err)
		return 1
	}
	return 0
}
