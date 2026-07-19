package workflowview

import (
	"strings"

	"core/server/metadata/sqlitegen"
	"core/server/workflow"
	"core/shared/clientui"
	"core/shared/serverapi"
)

type boardProjectWorkspaceFacts struct {
	primary   serverapi.ProjectWorkspaceSummary
	byID      map[string]serverapi.ProjectWorkspaceSummary
	count     int
	defaultID string
}

func projectBoardProject(project clientui.ProjectOverview, workspaceContext boardProjectWorkspaceFacts) serverapi.ProjectBoardProject {
	return serverapi.ProjectBoardProject{
		ProjectKey:             project.Project.ProjectKey,
		DisplayName:            project.Project.DisplayName,
		DefaultWorkspaceID:     workspaceContext.defaultID,
		AttachedWorkspaceCount: workspaceContext.count,
	}
}

func projectWorkspaceSummary(workspace clientui.ProjectWorkspaceSummary) serverapi.ProjectWorkspaceSummary {
	return serverapi.ProjectWorkspaceSummary{WorkspaceID: workspace.WorkspaceID, DisplayName: workspace.DisplayName, RootPath: workspace.RootPath, Availability: string(workspace.Availability), IsPrimary: workspace.IsPrimary, UpdatedAtUnixMs: workspace.UpdatedAt.UnixMilli()}
}

func boardProjectWorkspaceContext(project clientui.ProjectOverview) boardProjectWorkspaceFacts {
	context := boardProjectWorkspaceFacts{
		byID:  make(map[string]serverapi.ProjectWorkspaceSummary, len(project.Workspaces)),
		count: len(project.Workspaces),
	}
	for _, workspace := range project.Workspaces {
		dto := projectWorkspaceSummary(workspace)
		context.byID[dto.WorkspaceID] = dto
		if workspace.IsPrimary {
			context.primary = dto
			context.defaultID = dto.WorkspaceID
		}
	}
	return context
}

func sourceWorkspaceForTask(task sqlitegen.TaskRecord, workspacesByID map[string]serverapi.ProjectWorkspaceSummary, fallback serverapi.ProjectWorkspaceSummary) serverapi.ProjectWorkspaceSummary {
	if workspace, ok := workspacesByID[strings.TrimSpace(task.SourceWorkspaceID.String)]; ok {
		return workspace
	}
	snapshot := struct {
		SourceWorkspaceSnapshot struct {
			WorkspaceID string `json:"workspace_id"`
			DisplayName string `json:"display_name"`
			RootPath    string `json:"root_path"`
		} `json:"source_workspace_snapshot"`
	}{}
	if err := workflow.UnmarshalString(task.MetadataJson, &snapshot); err == nil {
		if strings.TrimSpace(snapshot.SourceWorkspaceSnapshot.RootPath) != "" {
			return serverapi.ProjectWorkspaceSummary{
				WorkspaceID:  strings.TrimSpace(snapshot.SourceWorkspaceSnapshot.WorkspaceID),
				DisplayName:  strings.TrimSpace(snapshot.SourceWorkspaceSnapshot.DisplayName),
				RootPath:     strings.TrimSpace(snapshot.SourceWorkspaceSnapshot.RootPath),
				Availability: string(clientui.ProjectAvailabilityUnlinked),
			}
		}
	}
	return fallback
}
