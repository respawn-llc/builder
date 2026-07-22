package invariant

import (
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
