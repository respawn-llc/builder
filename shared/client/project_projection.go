package client

import (
	"fmt"

	"core/shared/clientui"
	projectpb "core/shared/protoapi/gen/kent/api/project"
	"core/shared/runtimeids"
	"core/shared/serverapi"
	"core/shared/sessioncontract"
)

func ProjectAvailabilityFromProto(value projectpb.ProjectAvailability) (clientui.ProjectAvailability, error) {
	switch value {
	case projectpb.ProjectAvailability_PROJECT_AVAILABILITY_AVAILABLE:
		return clientui.ProjectAvailabilityAvailable, nil
	case projectpb.ProjectAvailability_PROJECT_AVAILABILITY_MISSING:
		return clientui.ProjectAvailabilityMissing, nil
	case projectpb.ProjectAvailability_PROJECT_AVAILABILITY_INACCESSIBLE:
		return clientui.ProjectAvailabilityInaccessible, nil
	case projectpb.ProjectAvailability_PROJECT_AVAILABILITY_UNLINKED:
		return clientui.ProjectAvailabilityUnlinked, nil
	default:
		return "", fmt.Errorf("unsupported Project availability %s", value)
	}
}

func ProjectSummaryFromProto(project *projectpb.ProjectSummary) (clientui.ProjectSummary, error) {
	availability, err := ProjectAvailabilityFromProto(project.Availability)
	if err != nil {
		return clientui.ProjectSummary{}, err
	}
	return clientui.ProjectSummary{
		ProjectID:    project.ProjectId,
		ProjectKey:   project.ProjectKey,
		DisplayName:  project.DisplayName,
		RootPath:     project.RootPath,
		Availability: availability,
		SessionCount: int(project.SessionCount),
		UpdatedAt:    project.UpdatedAt.AsTime(),
	}, nil
}

func ProjectSummariesFromProto(projects []*projectpb.ProjectSummary) ([]clientui.ProjectSummary, error) {
	result := make([]clientui.ProjectSummary, 0, len(projects))
	for _, project := range projects {
		summary, err := ProjectSummaryFromProto(project)
		if err != nil {
			return nil, err
		}
		result = append(result, summary)
	}
	return result, nil
}

func ProjectWorkspaceSummaryFromProto(workspace *projectpb.ProjectWorkspaceSummary) (clientui.ProjectWorkspaceSummary, error) {
	availability, err := ProjectAvailabilityFromProto(workspace.Availability)
	if err != nil {
		return clientui.ProjectWorkspaceSummary{}, err
	}
	return clientui.ProjectWorkspaceSummary{
		WorkspaceID:  workspace.WorkspaceId,
		DisplayName:  workspace.DisplayName,
		RootPath:     workspace.RootPath,
		Availability: availability,
		IsPrimary:    workspace.IsPrimary,
		SessionCount: int(workspace.SessionCount),
		UpdatedAt:    workspace.UpdatedAt.AsTime(),
	}, nil
}

func ProjectWorkspaceSummariesFromProto(workspaces []*projectpb.ProjectWorkspaceSummary) ([]clientui.ProjectWorkspaceSummary, error) {
	result := make([]clientui.ProjectWorkspaceSummary, 0, len(workspaces))
	for _, workspace := range workspaces {
		summary, err := ProjectWorkspaceSummaryFromProto(workspace)
		if err != nil {
			return nil, err
		}
		result = append(result, summary)
	}
	return result, nil
}

func ProjectBindingFromProto(binding *projectpb.ProjectBinding) (serverapi.ProjectBinding, error) {
	availability, err := ProjectAvailabilityFromProto(binding.WorkspaceStatus)
	if err != nil {
		return serverapi.ProjectBinding{}, err
	}
	return serverapi.ProjectBinding{
		ProjectID:       binding.ProjectId,
		ProjectKey:      binding.ProjectKey,
		ProjectName:     binding.ProjectName,
		WorkspaceID:     binding.WorkspaceId,
		CanonicalRoot:   binding.CanonicalRoot,
		WorkspaceName:   binding.WorkspaceName,
		WorkspaceStatus: string(availability),
	}, nil
}

func SessionCategoryToProto(category sessioncontract.SessionCategory) (projectpb.SessionCategory, error) {
	switch category {
	case sessioncontract.SessionCategoryMain:
		return projectpb.SessionCategory_SESSION_CATEGORY_MAIN, nil
	case sessioncontract.SessionCategorySubagent:
		return projectpb.SessionCategory_SESSION_CATEGORY_SUBAGENT, nil
	default:
		return projectpb.SessionCategory_SESSION_CATEGORY_UNSPECIFIED, fmt.Errorf("unsupported Session category %q", category)
	}
}

func SessionCategoryFromProto(category projectpb.SessionCategory) (sessioncontract.SessionCategory, error) {
	switch category {
	case projectpb.SessionCategory_SESSION_CATEGORY_MAIN:
		return sessioncontract.SessionCategoryMain, nil
	case projectpb.SessionCategory_SESSION_CATEGORY_SUBAGENT:
		return sessioncontract.SessionCategorySubagent, nil
	default:
		return "", fmt.Errorf("unsupported generated Session category %s", category)
	}
}

func SessionSummaryFromProto(session *projectpb.SessionSummary) (clientui.SessionSummary, error) {
	sessionID, err := runtimeids.ParseSessionID(session.SessionId)
	if err != nil {
		return clientui.SessionSummary{}, err
	}
	category, err := SessionCategoryFromProto(session.Category)
	if err != nil {
		return clientui.SessionSummary{}, err
	}
	return clientui.SessionSummary{
		SessionID:          sessionID,
		Category:           category,
		Name:               session.GetName(),
		FirstPromptPreview: session.GetFirstPromptPreview(),
		UpdatedAt:          session.UpdatedAt.AsTime(),
	}, nil
}

func SessionSummariesFromProto(sessions []*projectpb.SessionSummary) ([]clientui.SessionSummary, error) {
	result := make([]clientui.SessionSummary, 0, len(sessions))
	for _, session := range sessions {
		summary, err := SessionSummaryFromProto(session)
		if err != nil {
			return nil, err
		}
		result = append(result, summary)
	}
	return result, nil
}
