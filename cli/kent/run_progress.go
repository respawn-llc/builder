package main

import (
	"fmt"
	"io"
	"strings"

	"core/prompts"
	"core/shared/clientui"
	"core/shared/serverapi"
)

type runProgressRenderer struct {
	stdout               io.Writer
	stderr               io.Writer
	wroteStdoutBlock     bool
	finalResponseEmitted bool
}

func newRunProgressRenderer(stdout io.Writer, stderr io.Writer) *runProgressRenderer {
	return &runProgressRenderer{stdout: stdout, stderr: stderr}
}

func (r *runProgressRenderer) PublishRunPromptProgress(progress serverapi.RunPromptProgress) {
	if r == nil {
		return
	}
	switch progress.Kind {
	case serverapi.RunPromptProgressKindSessionStarted:
		if progress.SessionStarted == nil {
			return
		}
		_, _ = fmt.Fprintf(
			r.stderr,
			"Started a new session, `%s run steer %s \"prompt\"` to send messages while it runs\n",
			prompts.LaunchCommand(),
			progress.SessionStarted.SessionID,
		)
	case serverapi.RunPromptProgressKindAssistantMessage:
		if progress.AssistantMessage == nil {
			return
		}
		r.writeStdoutBlock(progress.AssistantMessage.Content)
		if progress.AssistantMessage.Phase == clientui.MessagePhaseFinal {
			r.finalResponseEmitted = true
		}
	case serverapi.RunPromptProgressKindSteeredMessage:
		if progress.SteeredMessage == nil {
			return
		}
		_, _ = fmt.Fprintf(r.stderr, "Steered message: %s\n", progress.SteeredMessage.Content)
	case serverapi.RunPromptProgressKindCompactionStarted:
		_, _ = fmt.Fprintln(r.stderr, "Compacting context")
	case serverapi.RunPromptProgressKindCompactionFailed:
		r.writeFailure("Context compaction failed", progress.Failure)
	case serverapi.RunPromptProgressKindRunLoggingFailed:
		r.writeFailure("Run logging degraded", progress.Failure)
	case serverapi.RunPromptProgressKindRunCleanupFailed:
		r.writeFailure("Run cleanup failed", progress.Failure)
	}
}

func (r *runProgressRenderer) writeFailure(summary string, failure *serverapi.RunPromptFailure) {
	if failure == nil || failure.Error == nil {
		_, _ = fmt.Fprintln(r.stderr, summary)
		return
	}
	_, _ = fmt.Fprintf(r.stderr, "%s: %s\n", summary, strings.TrimSpace(*failure.Error))
}

func (r *runProgressRenderer) Complete(result string, warnings []string, continueHint string) {
	if r == nil {
		return
	}
	emitWarnings(r.stderr, warnings)
	if !r.finalResponseEmitted {
		r.writeStdoutBlock(result)
	}
	r.writeStdoutBlock(continueHint)
}

func (r *runProgressRenderer) writeStdoutBlock(content string) {
	if r == nil || r.stdout == nil || strings.TrimSpace(content) == "" {
		return
	}
	if r.wroteStdoutBlock {
		_, _ = fmt.Fprintln(r.stdout)
	}
	_, _ = fmt.Fprintln(r.stdout, strings.TrimRight(content, "\r\n"))
	r.wroteStdoutBlock = true
}
