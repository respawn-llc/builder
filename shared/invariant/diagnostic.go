package invariant

import (
	"runtime/debug"
	"strings"
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
