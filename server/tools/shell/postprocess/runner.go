package postprocess

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"core/server/tools"
	"core/shared/config"
	"core/shared/toolspec"
)

type Settings struct {
	Mode     config.ShellPostprocessingMode
	HookPath *string
}

type Request struct {
	ToolName        toolspec.ID
	CommandText     string
	ParsedArgs      []string
	CommandName     string
	Workdir         string
	OwnerSessionID  string
	ExitCode        *int
	Raw             bool
	Output          string
	MaxDisplayChars int
	Backgrounded    bool
	Invocations     []tools.LiteralShellInvocation
}

type Result struct {
	Output             string
	Processed          bool
	ProcessorID        string
	Warning            Warning
	UnrecoverableError string
}

type warningMessage string

// Warning is an opaque immutable aggregate of non-empty operational warnings.
// Its unexported seal prevents callers outside this package from fabricating an
// empty present warning.
type Warning interface {
	warningSeal()
	Text() string
}

type warningAggregate struct {
	messages []warningMessage
}

func NewWarning(message string) (Warning, error) {
	normalized := strings.TrimSpace(message)
	if normalized == "" {
		return nil, errors.New("warning message is required")
	}
	return &warningAggregate{messages: []warningMessage{warningMessage(normalized)}}, nil
}

func (w *warningAggregate) warningSeal() {}

func (w *warningAggregate) Text() string {
	if w == nil || len(w.messages) == 0 {
		panic("present warning aggregate has no messages")
	}
	values := make([]string, len(w.messages))
	for index, message := range w.messages {
		values[index] = string(message)
	}
	return strings.Join(values, "\n")
}

func MergeWarnings(existing Warning, next Warning) Warning {
	if existing == nil {
		return cloneWarning(next)
	}
	if next == nil {
		return cloneWarning(existing)
	}
	existingAggregate := warningAggregateOf(existing)
	nextAggregate := warningAggregateOf(next)
	messages := make([]warningMessage, 0, len(existingAggregate.messages)+len(nextAggregate.messages))
	messages = append(messages, existingAggregate.messages...)
	messages = append(messages, nextAggregate.messages...)
	return &warningAggregate{messages: messages}
}

func cloneWarning(warning Warning) Warning {
	if warning == nil {
		return nil
	}
	aggregate := warningAggregateOf(warning)
	messages := append([]warningMessage(nil), aggregate.messages...)
	return &warningAggregate{messages: messages}
}

func warningAggregateOf(warning Warning) *warningAggregate {
	aggregate, ok := warning.(*warningAggregate)
	if !ok || aggregate == nil || len(aggregate.messages) == 0 {
		panic("present warning aggregate is invalid")
	}
	return aggregate
}

func mustWarning(message string) Warning {
	warning, err := NewWarning(message)
	if err != nil {
		panic(err)
	}
	return warning
}

type Processor interface {
	ID() string
	Process(context.Context, Envelope) (Decision, error)
}

type ScopedProcessor interface {
	Scope() Scope
}

type ExitCodeScope string

const (
	ExitCodeAny     ExitCodeScope = ""
	ExitCodeSuccess ExitCodeScope = "success"
	ExitCodeFailure ExitCodeScope = "failure"
)

type Scope struct {
	ToolNames    []toolspec.ID
	CommandNames []string
	ExitCodes    ExitCodeScope
}

func (s Scope) Matches(req Request) bool {
	if len(s.ToolNames) > 0 && !containsToolName(s.ToolNames, req.ToolName) {
		return false
	}
	if len(s.CommandNames) > 0 && !containsString(s.CommandNames, req.CommandName) {
		return false
	}
	switch s.ExitCodes {
	case ExitCodeSuccess:
		return req.ExitCode != nil && *req.ExitCode == 0
	case ExitCodeFailure:
		return req.ExitCode != nil && *req.ExitCode != 0
	default:
		return true
	}
}

func containsToolName(values []toolspec.ID, target toolspec.ID) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func containsString(values []string, target string) bool {
	target = strings.TrimSpace(target)
	for _, value := range values {
		if strings.TrimSpace(value) == target {
			return true
		}
	}
	return false
}

type Runner struct {
	mode             config.ShellPostprocessingMode
	hookPath         *string
	globalProcessors []Processor
	processors       []Processor
	hookProcessor    Processor
}

func NewRunner(settings Settings) (*Runner, error) {
	switch settings.Mode {
	case config.ShellPostprocessingModeNone, config.ShellPostprocessingModeBuiltin, config.ShellPostprocessingModeUser, config.ShellPostprocessingModeAll:
	default:
		return nil, fmt.Errorf("invalid shell postprocessing mode %q (expected none|builtin|user|all)", settings.Mode)
	}
	var hookPath *string
	if settings.HookPath != nil {
		normalizedHookPath := strings.TrimSpace(*settings.HookPath)
		if normalizedHookPath == "" {
			return nil, errors.New("shell postprocess hook cannot be empty; omit it to leave it unset")
		}
		hookPath = &normalizedHookPath
	}
	return &Runner{
		mode:             settings.Mode,
		hookPath:         hookPath,
		globalProcessors: []Processor{sanitizerProcessor{}},
		processors:       []Processor{gitWorktreeAdvisoryProcessor{}, goTestSuccessProcessor{}, fileReadContextProcessor{}},
	}, nil
}

func (r *Runner) PreservesRawOutput(raw bool) bool {
	if raw || r == nil {
		return true
	}
	return r.mode == config.ShellPostprocessingModeNone
}

func (r *Runner) Apply(ctx context.Context, req Request) (Result, error) {
	if req.Raw || r == nil || r.mode == config.ShellPostprocessingModeNone {
		return Result{Output: req.Output}, nil
	}
	request := normalizeRequest(req)

	envelope := NewEnvelope(request)
	processed := false
	processorID := ""

	mode := r.mode
	global, err := Chain{IDValue: "global", Processors: r.globalProcessors}.Process(ctx, envelope)
	if err != nil {
		return Result{}, err
	}
	envelope = global.Next
	processed = processed || global.Processed()
	if global.ProcessorID != "" {
		processorID = global.ProcessorID
	}
	if global.Failure != nil {
		return resultFromEnvelope(envelope, processed, processorID, *global.Failure), nil
	}

	if mode == config.ShellPostprocessingModeBuiltin || mode == config.ShellPostprocessingModeAll {
		builtin, err := Chain{IDValue: "builtin", Processors: r.processors}.Process(ctx, envelope)
		if err != nil {
			return Result{}, err
		}
		envelope = builtin.Next
		if builtin.Processed() {
			processed = true
			processorID = builtin.ProcessorID
		}
		if builtin.Failure != nil {
			return resultFromEnvelope(envelope, processed, processorID, *builtin.Failure), nil
		}
	}

	if mode == config.ShellPostprocessingModeUser || mode == config.ShellPostprocessingModeAll {
		hookProcessor := r.hookProcessor
		if hookProcessor == nil {
			hookProcessor = userHookProcessor{hookPath: r.hookPath}
		}
		hook, err := Chain{IDValue: "user", Processors: []Processor{hookProcessor}}.Process(ctx, envelope)
		if err != nil {
			return Result{}, err
		}
		envelope = hook.Next
		if hook.Processed() {
			processed = true
			processorID = hook.ProcessorID
		}
		if hook.Failure != nil {
			return resultFromEnvelope(envelope, processed, processorID, *hook.Failure), nil
		}
	}

	return resultFromEnvelope(envelope, processed, processorID, ProcessorFailure{}), nil
}

func normalizeRequest(req Request) Request {
	req.CommandText = strings.TrimSpace(req.CommandText)
	req.Workdir = strings.TrimSpace(req.Workdir)
	if len(req.Invocations) == 0 && req.CommandText != "" {
		req.Invocations = tools.ExtractLiteralShellInvocations(req.CommandText)
	}
	if len(req.ParsedArgs) == 0 && req.CommandText != "" {
		if parsed, ok := tools.ParseSimpleShellCommand(req.CommandText); ok {
			req.ParsedArgs = parsed
		}
	}
	if req.CommandName == "" && len(req.ParsedArgs) > 0 {
		req.CommandName = tools.NormalizeShellCommandName(req.ParsedArgs[0])
	}
	return req
}

func resolveHookPath(raw *string) (string, bool, error) {
	if raw == nil {
		return "", false, nil
	}
	trimmed := strings.TrimSpace(*raw)
	if trimmed == "" {
		return "", false, errors.New("present shell postprocess hook cannot be blank")
	}
	if strings.HasPrefix(trimmed, "~/") || trimmed == "~" {
		home, err := os.UserHomeDir()
		if err != nil || strings.TrimSpace(home) == "" {
			return "", false, nil
		}
		if trimmed == "~" {
			trimmed = home
		} else {
			trimmed = filepath.Join(home, strings.TrimPrefix(trimmed, "~/"))
		}
	}
	if filepath.IsAbs(trimmed) {
		return filepath.Clean(trimmed), true, nil
	}
	abs, err := filepath.Abs(trimmed)
	if err != nil {
		return "", false, nil
	}
	return abs, true, nil
}

type Action string

const (
	ActionSkip     Action = "skip"
	ActionContinue Action = "continue"
	ActionHalt     Action = "halt"
)

type Envelope struct {
	Request           Request
	OriginalOutput    string
	CurrentOutput     string
	Warnings          []Warning
	RecoverableErrors []ProcessorFailure
}

func NewEnvelope(req Request) Envelope {
	return Envelope{Request: req, OriginalOutput: req.Output, CurrentOutput: req.Output}
}

func (e Envelope) WithCurrent(output string) Envelope {
	e.CurrentOutput = output
	return e
}

func (e Envelope) WithOriginal(output string) Envelope {
	e.OriginalOutput = output
	return e
}

func (e Envelope) withWarning(warning Warning) Envelope {
	if warning != nil {
		e.Warnings = append(e.Warnings, cloneWarning(warning))
	}
	return e
}

func (e Envelope) withRecoverableFailure(failure ProcessorFailure) Envelope {
	if strings.TrimSpace(failure.Message) != "" {
		e.RecoverableErrors = append(e.RecoverableErrors, failure)
	}
	return e
}

type Decision struct {
	Action      Action
	Next        Envelope
	ProcessorID string
	Warning     Warning
	Failure     *ProcessorFailure
}

func (d Decision) Processed() bool {
	return d.Action == ActionContinue || d.Action == ActionHalt
}

func Skip(e Envelope) Decision {
	return Decision{Action: ActionSkip, Next: e}
}

func Continue(e Envelope, processorID string) Decision {
	return Decision{Action: ActionContinue, Next: e, ProcessorID: strings.TrimSpace(processorID)}
}

func Halt(e Envelope, processorID string) Decision {
	return Decision{Action: ActionHalt, Next: e, ProcessorID: strings.TrimSpace(processorID)}
}

type FailureSeverity string

const (
	FailureRecoverable    FailureSeverity = "recoverable"
	FailureUnrecoverable  FailureSeverity = "unrecoverable"
	FailureCritical       FailureSeverity = "critical"
	defaultProcessorError                 = "processor failed"
)

type ProcessorFailure struct {
	ProcessorID string
	Severity    FailureSeverity
	Message     string
}

type ProcessorError struct {
	Severity FailureSeverity
	Message  string
	Err      error
}

func (e ProcessorError) Error() string {
	if strings.TrimSpace(e.Message) != "" {
		return e.Message
	}
	if e.Err != nil {
		return e.Err.Error()
	}
	return defaultProcessorError
}

func (e ProcessorError) Unwrap() error {
	return e.Err
}

func IsCriticalError(err error) bool {
	var processorErr ProcessorError
	return errors.As(err, &processorErr) && processorErr.Severity == FailureCritical
}

type Chain struct {
	IDValue    string
	Processors []Processor
}

func (c Chain) Process(ctx context.Context, envelope Envelope) (Decision, error) {
	current := envelope
	processedID := ""
	processed := false
	for _, processor := range c.Processors {
		if processor == nil {
			continue
		}
		decision, err := Proxy(processor).Process(ctx, current)
		if err != nil {
			failure := classifyProcessorFailure(processor.ID(), err)
			if failure.Severity == FailureCritical {
				return Decision{Action: ActionHalt, Next: current, Failure: &failure}, err
			}
			if failure.Severity == FailureUnrecoverable {
				return Decision{Action: ActionHalt, Next: current, Failure: &failure}, nil
			}
			current = current.withRecoverableFailure(failure)
			continue
		}
		current = decision.Next.withWarning(decision.Warning)
		if decision.Processed() {
			processed = true
			processedID = decision.ProcessorID
			if processedID == "" {
				processedID = processor.ID()
			}
		}
		if decision.Failure != nil {
			failure := *decision.Failure
			if failure.ProcessorID == "" {
				failure.ProcessorID = processor.ID()
			}
			if failure.Severity == FailureCritical {
				return Decision{Action: ActionHalt, Next: current, ProcessorID: processedID, Failure: &failure}, ProcessorError{Severity: FailureCritical, Message: failure.Message}
			}
			if failure.Severity == FailureUnrecoverable {
				return Decision{Action: ActionHalt, Next: current, ProcessorID: processedID, Failure: &failure}, nil
			}
			current = current.withRecoverableFailure(failure)
		}
		if decision.Action == ActionHalt {
			return Decision{Action: ActionHalt, Next: current, ProcessorID: processedID}, nil
		}
	}
	action := ActionSkip
	if processed {
		action = ActionContinue
	}
	return Decision{Action: action, Next: current, ProcessorID: processedID}, nil
}

func Proxy(processor Processor) Processor {
	return processorProxy{inner: processor}
}

type processorProxy struct {
	inner Processor
}

func (p processorProxy) ID() string {
	if p.inner == nil {
		return "nil"
	}
	return p.inner.ID()
}

func (p processorProxy) Process(ctx context.Context, envelope Envelope) (decision Decision, err error) {
	if p.inner == nil {
		return Skip(envelope), nil
	}
	if scoped, ok := p.inner.(ScopedProcessor); ok && !scoped.Scope().Matches(envelope.Request) {
		return Skip(envelope), nil
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			err = ProcessorError{
				Severity: FailureCritical,
				Message:  fmt.Sprintf("postprocess processor %s panicked: %v", p.ID(), recovered),
			}
		}
	}()
	return p.inner.Process(ctx, envelope)
}

func classifyProcessorFailure(processorID string, err error) ProcessorFailure {
	severity := FailureRecoverable
	var processorErr ProcessorError
	if errors.As(err, &processorErr) {
		severity = processorErr.Severity
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		severity = FailureCritical
	}
	if severity == "" {
		severity = FailureRecoverable
	}
	return ProcessorFailure{ProcessorID: strings.TrimSpace(processorID), Severity: severity, Message: err.Error()}
}

func resultFromEnvelope(envelope Envelope, processed bool, processorID string, terminal ProcessorFailure) Result {
	var warning Warning
	for _, processorErr := range envelope.RecoverableErrors {
		warning = MergeWarnings(warning, mustWarning(formatProcessorFailure(processorErr)))
	}
	for _, item := range envelope.Warnings {
		warning = MergeWarnings(warning, item)
	}
	result := Result{Output: envelope.CurrentOutput, Processed: processed, ProcessorID: processorID, Warning: warning}
	if terminal.Severity == FailureUnrecoverable {
		result.UnrecoverableError = formatProcessorFailure(terminal)
	}
	return result
}

func formatProcessorFailure(failure ProcessorFailure) string {
	processorID := strings.TrimSpace(failure.ProcessorID)
	if processorID == "" {
		processorID = "unknown"
	}
	message := strings.TrimSpace(failure.Message)
	if message == "" {
		message = defaultProcessorError
	}
	return "Postprocess processor " + processorID + " failed: " + message
}
