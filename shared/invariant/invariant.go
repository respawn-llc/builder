package invariant

import (
	"os"
	"runtime/debug"
	"strings"
)

type Mode string

const (
	ModeDiagnostic Mode = "diagnostic"
	ModePanic      Mode = "panic"
)

type Scope string

const (
	ScopeTUIProjection        Scope = "tui_projection"
	ScopeReadModelPublication Scope = "read_model_publication"
)

type Field string

const (
	FieldOperation                Field = "operation"
	FieldSessionID                Field = "session_id"
	FieldCachedServerActivity     Field = "cached_server_activity"
	FieldLocalProjection          Field = "local_projection"
	FieldPendingInterrupt         Field = "pending_interrupt"
	FieldConnectionState          Field = "connection_state"
	FieldPublicationCause         Field = "publication_cause"
	FieldCurrentReadModelVersion  Field = "current_read_model_version"
	FieldProposedReadModelVersion Field = "proposed_read_model_version"
	FieldResolverInputs           Field = "resolver_inputs"
	FieldOwnerSnapshots           Field = "owner_snapshots"
	FieldCachedActivity           Field = "cached_activity"
	FieldResolvedActivity         Field = "resolved_activity"
	FieldProviderError            Field = "provider_error"
)

type Diagnostic struct {
	Scope  Scope
	Fields map[Field]string
	Stack  string
}

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

type TUIProjectionDiagnosticInput struct {
	Operation            string
	SessionID            string
	CachedServerActivity string
	LocalProjection      string
	PendingInterrupt     string
	ConnectionState      string
}

func TUIProjectionDiagnostic(input TUIProjectionDiagnosticInput) Diagnostic {
	return Diagnostic{
		Scope: ScopeTUIProjection,
		Fields: fields(map[Field]string{
			FieldOperation:            input.Operation,
			FieldSessionID:            input.SessionID,
			FieldCachedServerActivity: input.CachedServerActivity,
			FieldLocalProjection:      input.LocalProjection,
			FieldPendingInterrupt:     input.PendingInterrupt,
			FieldConnectionState:      input.ConnectionState,
		}),
	}
}

type ReadModelPublicationDiagnosticInput struct {
	Operation                   string
	SessionID                   string
	PublicationCause            string
	CurrentReadModelVersion     string
	ProposedReadModelVersion    string
	ResolverInputs              string
	OwnerSnapshots              string
	CachedLastPublishedActivity string
	ResolvedProposedActivity    string
	ProviderError               string
}

func ReadModelPublicationDiagnostic(input ReadModelPublicationDiagnosticInput) Diagnostic {
	return Diagnostic{
		Scope: ScopeReadModelPublication,
		Fields: fields(map[Field]string{
			FieldOperation:                input.Operation,
			FieldSessionID:                input.SessionID,
			FieldPublicationCause:         input.PublicationCause,
			FieldCurrentReadModelVersion:  input.CurrentReadModelVersion,
			FieldProposedReadModelVersion: input.ProposedReadModelVersion,
			FieldResolverInputs:           input.ResolverInputs,
			FieldOwnerSnapshots:           input.OwnerSnapshots,
			FieldCachedActivity:           input.CachedLastPublishedActivity,
			FieldResolvedActivity:         input.ResolvedProposedActivity,
			FieldProviderError:            input.ProviderError,
		}),
	}
}

func (d Diagnostic) withStack() Diagnostic {
	if d.Fields == nil {
		d.Fields = map[Field]string{}
	}
	if d.Stack == "" {
		d.Stack = string(debug.Stack())
	}
	return d
}

func fields(values map[Field]string) map[Field]string {
	out := make(map[Field]string, len(values))
	for key, value := range values {
		if strings.TrimSpace(value) == "" {
			continue
		}
		out[key] = value
	}
	return out
}

func modeFromEnvironment(getenv func(string) string) Mode {
	switch strings.ToLower(strings.TrimSpace(getenv("KENT_INVARIANT_MODE"))) {
	case string(ModePanic):
		return ModePanic
	case string(ModeDiagnostic):
		return ModeDiagnostic
	}
	if debugEnabled(getenv("KENT_DEBUG")) {
		return ModePanic
	}
	return ModeDiagnostic
}

func debugEnabled(raw string) bool {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}
