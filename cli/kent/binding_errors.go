package main

import (
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"core/shared/clientui"
	brand "core/shared/config"
	"core/shared/serverapi"
)

type sessionRetargetCandidateCommand struct {
	Project serverapi.ProjectReference
	Tokens  []string
}

type sessionRetargetCommandGuidance struct {
	Candidates       []sessionRetargetCandidateCommand
	AttachToSource   []string
	RebindIntoSource []string
}

func buildSessionRetargetCommandGuidance(targetPath string, retargetErr *serverapi.SessionRetargetError) sessionRetargetCommandGuidance {
	if retargetErr == nil {
		return sessionRetargetCommandGuidance{}
	}
	candidates := retargetErr.SortedCandidateProjects()
	guidance := sessionRetargetCommandGuidance{
		Candidates: make([]sessionRetargetCandidateCommand, 0, len(candidates)),
	}
	for _, candidate := range candidates {
		guidance.Candidates = append(guidance.Candidates, sessionRetargetCandidateCommand{
			Project: candidate,
			Tokens:  []string{brand.Command, "rebind", "--project", candidate.ID, retargetErr.SessionID, targetPath},
		})
	}
	if retargetErr.Reason == serverapi.SessionRetargetTargetProjectRequired {
		guidance.AttachToSource = []string{brand.Command, "attach", "--project", retargetErr.SourceProject.ID, targetPath}
		guidance.RebindIntoSource = []string{brand.Command, "rebind", retargetErr.SessionID, targetPath}
	}
	return guidance
}

func formatBindingCommandWorkspaceLabel(path string) string {
	trimmedPath := strings.TrimSpace(path)
	if trimmedPath == "" {
		trimmedPath = "."
	}
	absolutePath, err := filepath.Abs(trimmedPath)
	if err != nil {
		return trimmedPath
	}
	return absolutePath
}

func formatProjectLookupCommandError(path string, err error) error {
	if !errors.Is(err, errWorkspaceNotRegistered) {
		return err
	}
	return fmt.Errorf("%w: %s is not attached to a project", errWorkspaceNotRegistered, formatBindingCommandWorkspaceLabel(path))
}

func formatAttachWorkspaceCommandError(targetPath string, explicitProjectID string, err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, serverapi.ErrProjectNotFound):
		trimmedProjectID := strings.TrimSpace(explicitProjectID)
		if trimmedProjectID == "" {
			trimmedProjectID = "selected project"
		}
		return fmt.Errorf("project %q does not exist in this "+brand.Product+" state: %w", trimmedProjectID, err)
	case errors.Is(err, serverapi.ErrProjectUnavailable):
		if unavailable, ok := serverapi.AsProjectUnavailable(err); ok {
			switch unavailable.Availability {
			case clientui.ProjectAvailabilityMissing:
				return fmt.Errorf("project %q root %q is missing. Rebind affected sessions from their new workspace roots: %w", unavailable.ProjectID, unavailable.RootPath, err)
			case clientui.ProjectAvailabilityInaccessible:
				return fmt.Errorf("project %q root %q is inaccessible. Restore access or rebind affected sessions from another workspace root: %w", unavailable.ProjectID, unavailable.RootPath, err)
			}
		}
	case errors.Is(err, errWorkspaceNotRegistered):
		return err
	}
	_ = targetPath
	return err
}

func formatSessionRetargetCommandError(targetPath string, err error) error {
	var retargetErr *serverapi.SessionRetargetError
	if !errors.As(err, &retargetErr) {
		return err
	}
	switch retargetErr.Reason {
	case serverapi.SessionRetargetTargetProjectRequired:
		commands := buildSessionRetargetCommandGuidance(targetPath, retargetErr)
		var guidance strings.Builder
		_, _ = fmt.Fprintf(
			&guidance,
			"By default, %s rebind keeps session %s in its source project %q (%s).\n",
			brand.Command,
			strings.TrimSpace(retargetErr.SessionID),
			retargetErr.SourceProject.Name,
			retargetErr.SourceProject.ID,
		)
		_, _ = fmt.Fprintf(&guidance, "The target path %q is attached to another project.\n", targetPath)
		for _, candidate := range commands.Candidates {
			_, _ = fmt.Fprintf(
				&guidance,
				"Move the session to %q (%s): %s\n",
				candidate.Project.Name,
				candidate.Project.ID,
				shellCommand(candidate.Tokens...),
			)
		}
		_, _ = fmt.Fprintf(
			&guidance,
			"To keep the source project, attach the path first: %s\nThen rebind: %s",
			shellCommand(commands.AttachToSource...),
			shellCommand(commands.RebindIntoSource...),
		)
		return fmt.Errorf("%s\n%w", guidance.String(), err)
	case serverapi.SessionRetargetTargetProjectConflict:
		commands := buildSessionRetargetCommandGuidance(targetPath, retargetErr)
		var guidance strings.Builder
		_, _ = fmt.Fprintf(&guidance, "The target path %q is already attached to another project and cannot be auto-attached to the requested project.\n", targetPath)
		for _, candidate := range commands.Candidates {
			_, _ = fmt.Fprintf(
				&guidance,
				"Move the session using the existing %q (%s) binding: %s\n",
				candidate.Project.Name,
				candidate.Project.ID,
				shellCommand(candidate.Tokens...),
			)
		}
		return fmt.Errorf("%s\n%w", strings.TrimSuffix(guidance.String(), "\n"), err)
	case serverapi.SessionRetargetWorkflowOwned:
		taskIDs := append([]string(nil), retargetErr.WorkflowTaskIDs...)
		sort.Strings(taskIDs)
		if len(taskIDs) > 0 {
			return fmt.Errorf("session %s is owned by workflow task(s) %s and cannot move across projects: %w", retargetErr.SessionID, strings.Join(taskIDs, ", "), err)
		}
		return fmt.Errorf("session %s is workflow-owned and cannot move across projects: %w", retargetErr.SessionID, err)
	case serverapi.SessionRetargetBackgroundProcess:
		return fmt.Errorf("session %s has an active background process; stop it before rebinding: %w", retargetErr.SessionID, err)
	default:
		return err
	}
}
