package workflowstore

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"core/server/metadata/sqlitegen"
	"core/server/workflow"
	"core/server/workflowscript"
)

func taskManagedWorktreeRoot(ctx context.Context, q *sqlitegen.Queries, task sqlitegen.TaskRecord) (string, error) {
	worktreeID := strings.TrimSpace(task.ManagedWorktreeID.String)
	if !task.ManagedWorktreeID.Valid || worktreeID == "" {
		return "", nil
	}
	record, err := q.GetWorktreeByID(ctx, worktreeID)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(record.CanonicalRootPath), nil
}

func (s *Store) validateScriptNodeForExecution(ctx context.Context, q *sqlitegen.Queries, nodeID workflow.NodeID, executionRoot string) error {
	diagnostics, err := s.scriptNodeDiagnostics(ctx, q, nodeID, executionRoot)
	if err != nil {
		return err
	}
	for _, diagnostic := range diagnostics {
		if diagnostic.Blocking {
			return workflowscript.ValidationError{Diagnostic: diagnostic}
		}
	}
	return nil
}

func (s *Store) scriptNodeInterruption(ctx context.Context, q *sqlitegen.Queries, nodeID workflow.NodeID, executionRoot string) (reason string, detail string, invalid bool, err error) {
	diagnostics, err := s.scriptNodeDiagnostics(ctx, q, nodeID, executionRoot)
	if err != nil {
		return "", "", false, err
	}
	for _, diagnostic := range diagnostics {
		if !diagnostic.Blocking {
			continue
		}
		validationErr := workflowscript.ValidationError{Diagnostic: diagnostic}
		return workflowscript.ReasonValidationFailed, validationErr.DetailJSON(), true, nil
	}
	return "", "{}", false, nil
}

func (s *Store) scriptNodeDiagnostics(ctx context.Context, q *sqlitegen.Queries, nodeID workflow.NodeID, executionRoot string) ([]workflowscript.Diagnostic, error) {
	node, err := q.GetWorkflowNode(ctx, string(nodeID))
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("script node %q missing from workflow graph", nodeID)
		}
		return nil, err
	}
	if workflow.NodeKind(node.Kind) != workflow.NodeKindScript {
		return nil, nil
	}
	path := ""
	if node.ScriptPath.Valid {
		path = node.ScriptPath.String
	}
	return workflowscript.Validate(workflowscript.ValidationRequest{
		RawPath:              path,
		ExecutionRoot:        executionRoot,
		RequireExecutionRoot: true,
	}), nil
}
