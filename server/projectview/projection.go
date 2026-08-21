package projectview

import (
	"errors"
	"fmt"
	"math"
	"time"

	"core/server/metadata"
	"core/shared/clientui"
	projectpb "core/shared/protoapi/gen/kent/api/project"
	"core/shared/serverapi"
	"core/shared/sessioncontract"
	"core/shared/textutil"

	"google.golang.org/protobuf/types/known/timestamppb"
)

func projectSummaryToGenerated(summary clientui.ProjectSummary) (*projectpb.ProjectSummary, error) {
	availability, err := projectAvailabilityToGenerated(summary.Availability)
	if err != nil {
		return nil, err
	}
	sessionCount, err := nonNegativeInt32(summary.SessionCount, "project session count")
	if err != nil {
		return nil, err
	}
	return &projectpb.ProjectSummary{
		ProjectId:    summary.ProjectID,
		ProjectKey:   summary.ProjectKey,
		DisplayName:  summary.DisplayName,
		RootPath:     summary.RootPath,
		Availability: availability,
		SessionCount: sessionCount,
		UpdatedAt:    timestamppb.New(summary.UpdatedAt),
	}, nil
}

func projectWorkspaceSummaryToGenerated(summary clientui.ProjectWorkspaceSummary) (*projectpb.ProjectWorkspaceSummary, error) {
	availability, err := projectAvailabilityToGenerated(summary.Availability)
	if err != nil {
		return nil, err
	}
	sessionCount, err := nonNegativeInt32(summary.SessionCount, "project workspace session count")
	if err != nil {
		return nil, err
	}
	return &projectpb.ProjectWorkspaceSummary{
		WorkspaceId:  summary.WorkspaceID,
		DisplayName:  summary.DisplayName,
		RootPath:     summary.RootPath,
		Availability: availability,
		IsPrimary:    summary.IsPrimary,
		SessionCount: sessionCount,
		UpdatedAt:    timestamppb.New(summary.UpdatedAt),
	}, nil
}

func projectHomeSummaryToGenerated(summary serverapi.ProjectHomeSummary) (*projectpb.ProjectHomeSummary, error) {
	availability, err := projectAvailabilityToGenerated(clientui.ProjectAvailability(summary.PrimaryWorkspace.Availability))
	if err != nil {
		return nil, err
	}
	taskCount, err := nonNegativeInt32(summary.TaskCount, "project task count")
	if err != nil {
		return nil, err
	}
	attentionCount, err := nonNegativeInt32(summary.AttentionCount, "project attention count")
	if err != nil {
		return nil, err
	}
	workflowCount, err := nonNegativeInt32(summary.WorkflowCount, "project workflow count")
	if err != nil {
		return nil, err
	}
	project := &projectpb.ProjectHomeSummary{
		ProjectId:   summary.ProjectID,
		ProjectKey:  summary.ProjectKey,
		DisplayName: summary.DisplayName,
		PrimaryWorkspace: &projectpb.ProjectHomeWorkspaceSummary{
			WorkspaceId:  summary.PrimaryWorkspace.WorkspaceID,
			DisplayName:  summary.PrimaryWorkspace.DisplayName,
			RootPath:     summary.PrimaryWorkspace.RootPath,
			Availability: availability,
			IsPrimary:    summary.PrimaryWorkspace.IsPrimary,
			UpdatedAt:    timestamppb.New(time.UnixMilli(summary.PrimaryWorkspace.UpdatedAtUnixMs)),
		},
		DefaultWorkflowValid: summary.DefaultWorkflowValid,
		UpdatedAt:            timestamppb.New(time.UnixMilli(summary.UpdatedAtUnixMs)),
		TaskCount:            taskCount,
		AttentionCount:       attentionCount,
		WorkflowCount:        workflowCount,
	}
	if summary.DefaultWorkflowID != nil {
		workflowID := summary.DefaultWorkflowID.String()
		workflowName := summary.DefaultWorkflowName
		project.DefaultWorkflowId = &workflowID
		project.DefaultWorkflowName = &workflowName
	}
	return project, nil
}

func projectBindingToGenerated(binding metadata.Binding) (*projectpb.ProjectBinding, error) {
	availability, err := projectAvailabilityToGenerated(clientui.ProjectAvailability(binding.WorkspaceStatus))
	if err != nil {
		return nil, err
	}
	return &projectpb.ProjectBinding{
		ProjectId:       binding.ProjectID,
		ProjectKey:      binding.ProjectKey,
		ProjectName:     binding.ProjectName,
		WorkspaceId:     binding.WorkspaceID,
		CanonicalRoot:   binding.CanonicalRoot,
		WorkspaceName:   binding.WorkspaceName,
		WorkspaceStatus: availability,
	}, nil
}

func projectMutationBindingToGenerated(binding metadata.Binding) (*projectpb.ProjectMutationBinding, error) {
	availability, err := projectAvailabilityToGenerated(clientui.ProjectAvailability(binding.WorkspaceStatus))
	if err != nil {
		return nil, err
	}
	return &projectpb.ProjectMutationBinding{
		ProjectId:       binding.ProjectID,
		ProjectKey:      binding.ProjectKey,
		ProjectName:     binding.ProjectName,
		WorkspaceId:     binding.WorkspaceID,
		WorkspaceName:   binding.WorkspaceName,
		WorkspaceStatus: availability,
	}, nil
}

func projectWorkspaceCatalogRowToGenerated(workspace metadata.ProjectWorkspaceCatalogRow) *projectpb.ProjectWorkspaceCatalogSummary {
	return &projectpb.ProjectWorkspaceCatalogSummary{
		WorkspaceId: workspace.WorkspaceID,
		DisplayName: workspace.DisplayName,
		RootPath:    workspace.CanonicalRoot,
		IsDefault:   workspace.IsDefault,
	}
}

func projectWorkspaceSelectorFromGenerated(selector *projectpb.ProjectWorkspaceSelector) (serverapi.ProjectWorkspaceSelector, error) {
	if selector == nil {
		return serverapi.ProjectWorkspaceSelector{}, errors.New("project workspace selector is required")
	}
	switch value := selector.Selector.(type) {
	case *projectpb.ProjectWorkspaceSelector_WorkspaceId:
		return serverapi.NewProjectWorkspaceSelectorForID(value.WorkspaceId)
	case *projectpb.ProjectWorkspaceSelector_WorkspaceRoot:
		return serverapi.NewProjectWorkspaceSelectorForRoot(value.WorkspaceRoot)
	default:
		return serverapi.ProjectWorkspaceSelector{}, errors.New("project workspace selector is required")
	}
}

func projectWorkspaceGetSelectorFromGenerated(request *projectpb.GetProjectWorkspaceRequest) (serverapi.ProjectWorkspaceSelector, error) {
	switch value := request.Selector.(type) {
	case *projectpb.GetProjectWorkspaceRequest_WorkspaceId:
		return serverapi.NewProjectWorkspaceSelectorForID(value.WorkspaceId)
	case *projectpb.GetProjectWorkspaceRequest_WorkspaceRoot:
		return serverapi.NewProjectWorkspaceSelectorForRoot(value.WorkspaceRoot)
	default:
		return serverapi.ProjectWorkspaceSelector{}, errors.New("project workspace selector is required")
	}
}

func projectAvailabilityToGenerated(availability clientui.ProjectAvailability) (projectpb.ProjectAvailability, error) {
	switch availability {
	case clientui.ProjectAvailabilityAvailable:
		return projectpb.ProjectAvailability_PROJECT_AVAILABILITY_AVAILABLE, nil
	case clientui.ProjectAvailabilityMissing:
		return projectpb.ProjectAvailability_PROJECT_AVAILABILITY_MISSING, nil
	case clientui.ProjectAvailabilityInaccessible:
		return projectpb.ProjectAvailability_PROJECT_AVAILABILITY_INACCESSIBLE, nil
	case clientui.ProjectAvailabilityUnlinked:
		return projectpb.ProjectAvailability_PROJECT_AVAILABILITY_UNLINKED, nil
	default:
		return projectpb.ProjectAvailability_PROJECT_AVAILABILITY_UNSPECIFIED, fmt.Errorf("project availability %q is unsupported", availability)
	}
}

func sessionCategoryFromGenerated(category projectpb.SessionCategory) (sessioncontract.SessionCategory, error) {
	switch category {
	case projectpb.SessionCategory_SESSION_CATEGORY_MAIN:
		return sessioncontract.SessionCategoryMain, nil
	case projectpb.SessionCategory_SESSION_CATEGORY_SUBAGENT:
		return sessioncontract.SessionCategorySubagent, nil
	default:
		return "", fmt.Errorf("session category %v is unsupported", category)
	}
}

func sessionCategoryToGenerated(category sessioncontract.SessionCategory) (projectpb.SessionCategory, error) {
	switch category {
	case sessioncontract.SessionCategoryMain:
		return projectpb.SessionCategory_SESSION_CATEGORY_MAIN, nil
	case sessioncontract.SessionCategorySubagent:
		return projectpb.SessionCategory_SESSION_CATEGORY_SUBAGENT, nil
	default:
		return projectpb.SessionCategory_SESSION_CATEGORY_UNSPECIFIED, fmt.Errorf("session category %q is unsupported", category)
	}
}

func sessionPageToGenerated(page metadata.SessionPage) (*projectpb.SessionPageSuccess, error) {
	category, err := sessionCategoryToGenerated(page.Category)
	if err != nil {
		return nil, err
	}
	success := &projectpb.SessionPageSuccess{
		ProjectId: page.ProjectID,
		Category:  category,
		Sessions:  make([]*projectpb.SessionSummary, 0, len(page.Sessions)),
	}
	for _, summary := range page.Sessions {
		summaryCategory, err := sessionCategoryToGenerated(summary.Category)
		if err != nil {
			return nil, err
		}
		session := &projectpb.SessionSummary{
			SessionId: summary.SessionID.String(),
			Category:  summaryCategory,
			Name:      textutil.Pointer(summary.Name),
			FirstPromptPreview: textutil.Pointer(
				summary.FirstPromptPreview,
			),
			UpdatedAt: timestamppb.New(summary.UpdatedAt),
		}
		success.Sessions = append(success.Sessions, session)
	}
	if page.NextOffset != nil {
		nextOffset, err := nonNegativeInt32(*page.NextOffset, "session next offset")
		if err != nil {
			return nil, err
		}
		success.NextOffset = &nextOffset
	}
	return success, nil
}

func nonNegativeInt32(value int, field string) (int32, error) {
	if value < 0 || value > math.MaxInt32 {
		return 0, fmt.Errorf("%s %d is outside int32 range", field, value)
	}
	return int32(value), nil
}
