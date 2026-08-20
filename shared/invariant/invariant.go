package invariant

import (
	"errors"
	"fmt"
	"os"
)

type Mode string

const (
	ModeDiagnostic Mode = "diagnostic"
	ModePanic      Mode = "panic"
)

type Sink interface {
	RecordInvariantDiagnostic(Diagnostic)
}

type SinkFunc func(Diagnostic)

func (f SinkFunc) RecordInvariantDiagnostic(d Diagnostic) {
	f(d)
}

type Policy struct {
	mode Mode
	sink Sink
}

type Option func(*policyConfig)

func OperationalError(sentinel error, debug bool, operation string, cause error) error {
	return OperationalPolicy(debug).OperationalError(sentinel, operation, cause)
}

func (p Policy) OperationalError(sentinel error, operation string, cause error) error {
	if sentinel == nil {
		sentinel = errors.New("ownership invariant failed")
	}
	p.Check(false, FailureDiagnostic(
		ScopeWorkflowExecution,
		operation,
		cause,
	))
	return fmt.Errorf("%w: %s: %v", sentinel, operation, cause)
}

func OperationalPolicy(debug bool) Policy {
	mode := ModeDiagnostic
	if debug {
		mode = ModePanic
	}
	return NewPolicy(WithMode(mode))
}

type policyConfig struct {
	modeSet bool
	mode    Mode
	sink    Sink
	getenv  func(string) string
}

func NewPolicy(options ...Option) Policy {
	config := policyConfig{
		sink:   SinkFunc(func(Diagnostic) {}),
		getenv: os.Getenv,
	}
	for _, option := range options {
		option(&config)
	}
	mode := config.mode
	if !config.modeSet {
		mode = modeFromEnvironment(config.getenv)
	}
	if mode != ModePanic {
		mode = ModeDiagnostic
	}
	return Policy{mode: mode, sink: config.sink}
}

func WithMode(mode Mode) Option {
	return func(config *policyConfig) {
		config.modeSet = true
		config.mode = mode
	}
}

func WithSink(sink Sink) Option {
	return func(config *policyConfig) {
		if sink != nil {
			config.sink = sink
		}
	}
}

func WithEnvironment(getenv func(string) string) Option {
	return func(config *policyConfig) {
		if getenv != nil {
			config.getenv = getenv
		}
	}
}

func (p Policy) Mode() Mode {
	return p.mode
}

func (p Policy) Check(condition bool, diagnostic Diagnostic) {
	if condition {
		return
	}
	diagnostic = diagnostic.withStack()
	if p.mode == ModePanic {
		panic(diagnostic)
	}
	if p.sink != nil {
		p.sink.RecordInvariantDiagnostic(diagnostic)
	}
}
