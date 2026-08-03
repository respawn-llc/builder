package core

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"core/server/metadata"
	"core/server/promptcommands"
	"core/server/runtimecontrol"
	"core/shared/apicontract"
	"core/shared/clientui"
	"core/shared/serverapi"
)

type promptCommandCatalogService struct {
	catalog promptcommands.Service
}

func (s promptCommandCatalogService) GetPromptCommandCatalog(context.Context, serverapi.PromptCommandCatalogRequest) (serverapi.PromptCommandCatalogResponse, error) {
	entries, err := s.catalog.Catalog()
	if err != nil {
		return serverapi.PromptCommandCatalogResponse{}, publicPromptCommandError(err)
	}
	response := serverapi.PromptCommandCatalogResponse{Commands: append([]serverapi.PromptCommandCatalogEntry(nil), entries...)}
	return response, response.Validate()
}

type promptCommandRuntimeResolver struct {
	effectiveWorkspace promptCommandEffectiveWorkspaceResolver
	metadataStore      *metadata.Store
}

type promptCommandEffectiveWorkspaceResolver struct {
	persistenceRoot string
}

func promptCommandWorkspaceRoot(target clientui.SessionExecutionTarget, fallback string) (string, error) {
	if target.Worktree == nil {
		return fallback, nil
	}
	root := strings.TrimSpace(target.Worktree.Root)
	if root == "" {
		return "", errors.New("session execution worktree root is required")
	}
	return root, nil
}

func (r promptCommandEffectiveWorkspaceResolver) ResolvePromptCommandForWorkspace(ctx context.Context, workspaceRoot, name, arguments string) (string, error) {
	content, err := promptcommands.New(r.persistenceRoot, workspaceRoot).Resolve(name, arguments)
	if err != nil {
		return "", publicPromptCommandError(err)
	}
	return content, nil
}

func (r promptCommandRuntimeResolver) ResolvePromptCommand(ctx context.Context, sessionID, name, arguments string) (string, error) {
	if r.metadataStore == nil {
		return "", errors.New("metadata store is required")
	}
	target, err := r.metadataStore.ResolveSessionExecutionTarget(ctx, strings.TrimSpace(sessionID))
	if err != nil {
		return "", err
	}
	binding, err := r.metadataStore.LookupWorkspaceBindingByID(ctx, target.WorkspaceID)
	if err != nil {
		return "", err
	}
	workspaceRoot, err := promptCommandWorkspaceRoot(target, binding.CanonicalRoot)
	if err != nil {
		return "", err
	}
	return r.effectiveWorkspace.ResolvePromptCommandForWorkspace(ctx, workspaceRoot, name, arguments)
}

var _ runtimecontrol.PromptCommandResolver = promptCommandRuntimeResolver{}

func publicPromptCommandError(err error) error {
	var source *promptcommands.Error
	if !errors.As(err, &source) {
		return err
	}
	kind := serverapi.PromptCommandErrorKindCatalogRead
	switch source.Kind {
	case promptcommands.ErrorKindCommandNotFound:
		kind = serverapi.PromptCommandErrorKindCommandNotFound
	case promptcommands.ErrorKindCommandRead:
		kind = serverapi.PromptCommandErrorKindCommandRead
	}
	return &promptCommandPublicError{
		err:   &serverapi.PromptCommandError{Kind: kind, Command: source.Command},
		cause: source,
	}
}

type promptCommandPublicError struct {
	err   *serverapi.PromptCommandError
	cause error
}

func (e *promptCommandPublicError) Error() string {
	return e.err.Error()
}

func (e *promptCommandPublicError) As(target any) bool {
	if typed, ok := target.(**serverapi.PromptCommandError); ok {
		*typed = e.err
		return true
	}
	return false
}

func (e *promptCommandPublicError) diagnosticCause() error {
	return e.cause
}

func (e *promptCommandPublicError) RPCErrorCode() int {
	return e.err.RPCErrorCode()
}

func (e *promptCommandPublicError) RPCErrorData() json.RawMessage {
	return e.err.RPCErrorData()
}

func (s *Core) PromptCommandCatalogClientForProjectWorkspace(ctx context.Context, projectID, workspaceRoot string) (apicontract.PromptCommandCatalogService, error) {
	projectCtx, err := s.resolveProjectContext(ctx, projectID, "", workspaceRoot)
	if err != nil {
		return nil, err
	}
	return promptCommandCatalogService{
		catalog: promptcommands.New(projectCtx.config.PersistenceRoot, projectCtx.projectRoot),
	}, nil
}
