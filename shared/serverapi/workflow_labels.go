package serverapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"core/shared/labelcontract"
	"core/shared/protocol"
	"core/shared/runtimeids"
)

const WorkflowLabelMaxIDs = labelcontract.MaxProjectLabels

type WorkflowProjectLabel struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type WorkflowProjectLabelCatalog struct {
	ProjectID string                 `json:"project_id"`
	Labels    []WorkflowProjectLabel `json:"labels"`
}

type WorkflowProjectLabelCatalogRequest struct {
	ProjectID string `json:"project_id"`
}

type WorkflowProjectLabelReorderRequest struct {
	ProjectID string   `json:"project_id"`
	LabelIDs  []string `json:"label_ids"`
}

type WorkflowProjectLabelCatalogResponse struct {
	Catalog WorkflowProjectLabelCatalog `json:"catalog"`
}

type WorkflowProjectLabelCreateRequest struct {
	ProjectID string `json:"project_id"`
	Name      string `json:"name"`
}

type WorkflowProjectLabelCreateResponse struct {
	Label WorkflowProjectLabel `json:"label"`
}

type WorkflowProjectLabelRenameRequest struct {
	ProjectID string `json:"project_id"`
	LabelID   string `json:"label_id"`
	Name      string `json:"name"`
}

type WorkflowProjectLabelRenameResponse struct {
	Label WorkflowProjectLabel `json:"label"`
}

type WorkflowProjectLabelDeleteRequest struct {
	ProjectID string `json:"project_id"`
	LabelID   string `json:"label_id"`
}

type WorkflowProjectLabelDeleteResponse struct {
	LabelID string `json:"label_id"`
}

type WorkflowProjectLabelReorderRequest struct {
	ProjectID string   `json:"project_id"`
	LabelIDs  []string `json:"label_ids"`
}

type WorkflowProjectLabelReorderResponse struct {
	Catalog WorkflowProjectLabelCatalog `json:"catalog"`
}

type WorkflowTaskAssignedLabelIDs struct {
	TaskID   string   `json:"task_id"`
	LabelIDs []string `json:"label_ids"`
}

type WorkflowTaskLabelsGetRequest struct {
	TaskID string `json:"task_id"`
}

type WorkflowTaskLabelsGetResponse struct {
	Assignment WorkflowTaskAssignedLabelIDs `json:"assignment"`
}

type WorkflowTaskLabelsUpdateRequest struct {
	TaskID         string   `json:"task_id"`
	AddLabelIDs    []string `json:"add_label_ids"`
	RemoveLabelIDs []string `json:"remove_label_ids"`
}

type WorkflowTaskLabelsUpdateResponse struct {
	Assignment WorkflowTaskAssignedLabelIDs `json:"assignment"`
}

type WorkflowTaskLabelFilterKind string

const (
	WorkflowTaskLabelFilterKindNone      WorkflowTaskLabelFilterKind = "none"
	WorkflowTaskLabelFilterKindNamed     WorkflowTaskLabelFilterKind = "named"
	WorkflowTaskLabelFilterKindUnlabeled WorkflowTaskLabelFilterKind = "unlabeled"
)

type WorkflowTaskNamedLabelFilterMode string

const (
	WorkflowTaskNamedLabelFilterModeAny WorkflowTaskNamedLabelFilterMode = "any"
	WorkflowTaskNamedLabelFilterModeAll WorkflowTaskNamedLabelFilterMode = "all"
)

type WorkflowTaskNamedLabelFilter struct {
	Mode             WorkflowTaskNamedLabelFilterMode `json:"mode"`
	LabelIDs         []string                         `json:"label_ids"`
	ExcludedLabelIDs []string                         `json:"excluded_label_ids,omitempty"`
}

type WorkflowTaskLabelFilter struct {
	Kind  WorkflowTaskLabelFilterKind   `json:"kind"`
	Named *WorkflowTaskNamedLabelFilter `json:"named,omitempty"`
}

func WorkflowTaskLabelFilterNone() WorkflowTaskLabelFilter {
	return WorkflowTaskLabelFilter{Kind: WorkflowTaskLabelFilterKindNone}
}

type WorkflowLabelErrorReason string

const (
	WorkflowLabelErrorReasonInvalidName     WorkflowLabelErrorReason = "invalid_name"
	WorkflowLabelErrorReasonNameConflict    WorkflowLabelErrorReason = "name_conflict"
	WorkflowLabelErrorReasonCatalogLimit    WorkflowLabelErrorReason = "catalog_limit"
	WorkflowLabelErrorReasonProjectNotFound WorkflowLabelErrorReason = "project_not_found"
	WorkflowLabelErrorReasonLabelNotFound   WorkflowLabelErrorReason = "label_not_found"
	WorkflowLabelErrorReasonTaskNotFound    WorkflowLabelErrorReason = "task_not_found"
	WorkflowLabelErrorReasonWrongProject    WorkflowLabelErrorReason = "wrong_project"
	WorkflowLabelErrorReasonInvalidFilter   WorkflowLabelErrorReason = "invalid_filter"
	WorkflowLabelErrorReasonInvalidMutation WorkflowLabelErrorReason = "invalid_mutation"
)

type WorkflowLabelError struct {
	Reason    WorkflowLabelErrorReason
	ProjectID *string
	TaskID    *string
	LabelID   *string
	Field     *string
	Limit     *int
}

func (e *WorkflowLabelError) Error() string {
	if e == nil {
		return "workflow label error"
	}
	return "workflow label error: " + string(e.Reason)
}

func (e *WorkflowLabelError) RPCErrorCode() int {
	return protocol.ErrCodeWorkflowLabel
}

func (e *WorkflowLabelError) RPCErrorData() json.RawMessage {
	if e == nil {
		return nil
	}
	return marshalRPCErrorData(struct {
		Type      string                   `json:"type"`
		Reason    WorkflowLabelErrorReason `json:"reason"`
		ProjectID *string                  `json:"project_id,omitempty"`
		TaskID    *string                  `json:"task_id,omitempty"`
		LabelID   *string                  `json:"label_id,omitempty"`
		Field     *string                  `json:"field,omitempty"`
		Limit     *int                     `json:"limit,omitempty"`
	}{
		Type:      "workflow_label_error",
		Reason:    e.Reason,
		ProjectID: e.ProjectID,
		TaskID:    e.TaskID,
		LabelID:   e.LabelID,
		Field:     e.Field,
		Limit:     e.Limit,
	})
}

func DecodeWorkflowLabelError(data json.RawMessage, message string) error {
	var envelope struct {
		Type      string                   `json:"type"`
		Reason    WorkflowLabelErrorReason `json:"reason"`
		ProjectID *string                  `json:"project_id,omitempty"`
		TaskID    *string                  `json:"task_id,omitempty"`
		LabelID   *string                  `json:"label_id,omitempty"`
		Field     *string                  `json:"field,omitempty"`
		Limit     *int                     `json:"limit,omitempty"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil || envelope.Type != "workflow_label_error" {
		return errors.New(strings.TrimSpace(message))
	}
	decoded := WorkflowLabelError{
		Reason:    envelope.Reason,
		ProjectID: envelope.ProjectID,
		TaskID:    envelope.TaskID,
		LabelID:   envelope.LabelID,
		Field:     envelope.Field,
		Limit:     envelope.Limit,
	}
	if !validWorkflowLabelError(decoded) {
		return errors.New(strings.TrimSpace(message))
	}
	return &decoded
}

func validWorkflowLabelError(errorEnvelope WorkflowLabelError) bool {
	projectIDValid := validWorkflowLabelErrorString(errorEnvelope.ProjectID)
	taskIDValid := validWorkflowLabelErrorString(errorEnvelope.TaskID)
	fieldValid := validWorkflowLabelErrorString(errorEnvelope.Field)
	labelIDValid := errorEnvelope.LabelID != nil && validateLabelID("label_id", *errorEnvelope.LabelID) == nil
	switch errorEnvelope.Reason {
	case WorkflowLabelErrorReasonInvalidName:
		return projectIDValid &&
			errorEnvelope.Field != nil &&
			*errorEnvelope.Field == "name" &&
			errorEnvelope.TaskID == nil &&
			errorEnvelope.LabelID == nil &&
			errorEnvelope.Limit == nil
	case WorkflowLabelErrorReasonNameConflict:
		return projectIDValid &&
			errorEnvelope.TaskID == nil &&
			errorEnvelope.LabelID == nil &&
			errorEnvelope.Field == nil &&
			errorEnvelope.Limit == nil
	case WorkflowLabelErrorReasonCatalogLimit:
		return projectIDValid &&
			errorEnvelope.Limit != nil &&
			*errorEnvelope.Limit == WorkflowLabelMaxIDs &&
			errorEnvelope.TaskID == nil &&
			errorEnvelope.LabelID == nil &&
			errorEnvelope.Field == nil
	case WorkflowLabelErrorReasonProjectNotFound:
		return projectIDValid &&
			errorEnvelope.TaskID == nil &&
			errorEnvelope.LabelID == nil &&
			errorEnvelope.Field == nil &&
			errorEnvelope.Limit == nil
	case WorkflowLabelErrorReasonLabelNotFound:
		return labelIDValid &&
			(errorEnvelope.ProjectID == nil || projectIDValid) &&
			errorEnvelope.TaskID == nil &&
			errorEnvelope.Field == nil &&
			errorEnvelope.Limit == nil
	case WorkflowLabelErrorReasonTaskNotFound:
		return taskIDValid &&
			errorEnvelope.ProjectID == nil &&
			errorEnvelope.LabelID == nil &&
			errorEnvelope.Field == nil &&
			errorEnvelope.Limit == nil
	case WorkflowLabelErrorReasonWrongProject:
		return projectIDValid &&
			labelIDValid &&
			(errorEnvelope.TaskID == nil || taskIDValid) &&
			errorEnvelope.Field == nil &&
			errorEnvelope.Limit == nil
	case WorkflowLabelErrorReasonInvalidFilter:
		return fieldValid &&
			errorEnvelope.ProjectID == nil &&
			errorEnvelope.TaskID == nil &&
			errorEnvelope.LabelID == nil &&
			errorEnvelope.Limit == nil
	case WorkflowLabelErrorReasonInvalidMutation:
		return fieldValid &&
			(errorEnvelope.ProjectID == nil || projectIDValid) &&
			(errorEnvelope.TaskID == nil || taskIDValid) &&
			(errorEnvelope.LabelID == nil || labelIDValid) &&
			(errorEnvelope.Limit == nil || *errorEnvelope.Limit > 0)
	default:
		return false
	}
}

func validWorkflowLabelErrorString(value *string) bool {
	return value != nil && strings.TrimSpace(*value) != "" && strings.TrimSpace(*value) == *value
}

func (r WorkflowProjectLabelCatalogRequest) Validate() error {
	return validateRequired("project_id", r.ProjectID)
}

func (r WorkflowProjectLabelReorderRequest) Validate() error {
	if err := validateRequired("project_id", r.ProjectID); err != nil {
		return err
	}
	return validateLabelIDs("label_ids", r.LabelIDs)
}

func (r WorkflowProjectLabelReorderRequest) ValidateRPC() error {
	return workflowLabelRPCValidationError(r.Validate(), r.ProjectID, "", false)
}

func (r WorkflowProjectLabel) Validate() error {
	if err := validateLabelID("id", r.ID); err != nil {
		return err
	}
	return validateRequired("name", r.Name)
}

func (r WorkflowProjectLabelCatalog) Validate() error {
	if err := validateRequired("project_id", r.ProjectID); err != nil {
		return err
	}
	return validateProjectLabels("labels", r.Labels)
}

func (r WorkflowProjectLabelCatalogResponse) Validate() error {
	return r.Catalog.Validate()
}

func (r WorkflowProjectLabelCreateRequest) Validate() error {
	return validateRequiredFields(
		requiredField("project_id", r.ProjectID),
		requiredField("name", r.Name),
	)
}

func (r WorkflowProjectLabelCreateRequest) ValidateRPC() error {
	return workflowLabelRPCValidationError(r.Validate(), r.ProjectID, "", true)
}

func (r WorkflowProjectLabelRenameRequest) Validate() error {
	if err := validateRequiredFields(
		requiredField("project_id", r.ProjectID),
		requiredField("name", r.Name),
	); err != nil {
		return err
	}
	return validateLabelID("label_id", r.LabelID)
}

func (r WorkflowProjectLabelRenameRequest) ValidateRPC() error {
	return workflowLabelRPCValidationError(r.Validate(), r.ProjectID, "", true)
}

func (r WorkflowProjectLabelDeleteRequest) Validate() error {
	if err := validateRequired("project_id", r.ProjectID); err != nil {
		return err
	}
	return validateLabelID("label_id", r.LabelID)
}

func (r WorkflowProjectLabelDeleteRequest) ValidateRPC() error {
	return workflowLabelRPCValidationError(r.Validate(), r.ProjectID, "", false)
}

func (r WorkflowProjectLabelCreateResponse) Validate() error {
	return r.Label.Validate()
}

func (r WorkflowProjectLabelRenameResponse) Validate() error {
	return r.Label.Validate()
}

func (r WorkflowProjectLabelDeleteResponse) Validate() error {
	return validateLabelID("label_id", r.LabelID)
}

func (r WorkflowProjectLabelReorderRequest) Validate() error {
	if err := validateRequired("project_id", r.ProjectID); err != nil {
		return err
	}
	_, err := validateUniqueLabelIDs("label_ids", r.LabelIDs)
	return err
}

func (r WorkflowProjectLabelReorderRequest) ValidateRPC() error {
	return workflowLabelRPCValidationError(r.Validate(), r.ProjectID, "", true)
}

func (r WorkflowProjectLabelReorderResponse) Validate() error {
	return r.Catalog.Validate()
}

func (r WorkflowTaskLabelsGetRequest) Validate() error {
	return validateRequired("task_id", r.TaskID)
}

func (r WorkflowTaskAssignedLabelIDs) Validate() error {
	if err := validateRequired("task_id", r.TaskID); err != nil {
		return err
	}
	return validateLabelIDs("label_ids", r.LabelIDs)
}

func (r WorkflowTaskLabelsGetResponse) Validate() error {
	return r.Assignment.Validate()
}

func (r WorkflowTaskLabelsUpdateRequest) Validate() error {
	if err := validateRequired("task_id", r.TaskID); err != nil {
		return err
	}
	added, err := validateUniqueLabelIDs("add_label_ids", r.AddLabelIDs)
	if err != nil {
		return err
	}
	if _, err := validateUniqueLabelIDs("remove_label_ids", r.RemoveLabelIDs); err != nil {
		return err
	}
	for index, labelID := range r.RemoveLabelIDs {
		if added[labelID] {
			return workflowRequestError(
				WorkflowRequestErrorInvalidValue,
				fmt.Sprintf("remove_label_ids[%d]", index),
				"label ID cannot be both added and removed",
			)
		}
	}
	return nil
}

func (r WorkflowTaskLabelsUpdateRequest) ValidateRPC() error {
	return workflowLabelRPCValidationError(r.Validate(), "", r.TaskID, false)
}

func (r WorkflowTaskLabelsUpdateResponse) Validate() error {
	return r.Assignment.Validate()
}

func (r WorkflowTaskLabelFilter) Validate() error {
	switch r.Kind {
	case WorkflowTaskLabelFilterKindNone, WorkflowTaskLabelFilterKindUnlabeled:
		if r.Named != nil {
			return workflowRequestError(WorkflowRequestErrorInvalidValue, "label_filter.named", "named filter data is only valid when kind is named")
		}
		return nil
	case WorkflowTaskLabelFilterKindNamed:
		if r.Named == nil {
			return workflowRequestError(WorkflowRequestErrorRequired, "label_filter.named", "named filter data is required when kind is named")
		}
		switch r.Named.Mode {
		case WorkflowTaskNamedLabelFilterModeAny, WorkflowTaskNamedLabelFilterModeAll:
		default:
			return workflowRequestError(WorkflowRequestErrorInvalidValue, "label_filter.named.mode", "named filter mode must be any or all")
		}
		if len(r.Named.LabelIDs)+len(r.Named.ExcludedLabelIDs) == 0 {
			return workflowRequestError(WorkflowRequestErrorRequired, "label_filter.label_ids", "named filter label_ids is required")
		}
		if len(r.Named.LabelIDs)+len(r.Named.ExcludedLabelIDs) > WorkflowLabelMaxIDs {
			field := "label_filter.label_ids"
			if len(r.Named.ExcludedLabelIDs) > 0 {
				field = "label_filter.excluded_label_ids"
			}
			return workflowRequestError(
				WorkflowRequestErrorTooLong,
				field,
				fmt.Sprintf("named filter must contain at most %d label IDs", WorkflowLabelMaxIDs),
			)
		}
		included, err := validateUniqueLabelIDs("label_filter.label_ids", r.Named.LabelIDs)
		if err != nil {
			return err
		}
		if _, err := validateUniqueLabelIDs("label_filter.excluded_label_ids", r.Named.ExcludedLabelIDs); err != nil {
			return err
		}
		for index, labelID := range r.Named.ExcludedLabelIDs {
			if included[labelID] {
				return workflowRequestError(
					WorkflowRequestErrorInvalidValue,
					fmt.Sprintf("label_filter.excluded_label_ids[%d]", index),
					"label ID cannot be both included and excluded",
				)
			}
		}
		return nil
	case "":
		return workflowRequestError(WorkflowRequestErrorRequired, "label_filter.kind", "label filter kind is required")
	default:
		return workflowRequestError(WorkflowRequestErrorInvalidValue, "label_filter.kind", "label filter kind must be none, named, or unlabeled")
	}
}

func (r WorkflowTaskLabelFilter) ValidateRPC() error {
	return workflowLabelFilterRPCValidationError(r.Validate())
}

func validateUniqueLabelIDs(field string, ids []string) (map[string]bool, error) {
	if len(ids) > WorkflowLabelMaxIDs {
		return nil, workflowRequestError(
			WorkflowRequestErrorTooLong,
			field,
			fmt.Sprintf("%s must contain at most %d label IDs", field, WorkflowLabelMaxIDs),
		)
	}
	seen := make(map[string]bool, len(ids))
	for index, id := range ids {
		if err := validateLabelID(fmt.Sprintf("%s[%d]", field, index), id); err != nil {
			return nil, err
		}
		if seen[id] {
			return nil, workflowRequestError(WorkflowRequestErrorInvalidValue, fmt.Sprintf("%s[%d]", field, index), "label IDs must be unique")
		}
		seen[id] = true
	}
	return seen, nil
}

func validateProjectLabels(field string, labels []WorkflowProjectLabel) error {
	if len(labels) > WorkflowLabelMaxIDs {
		return workflowRequestError(
			WorkflowRequestErrorTooLong,
			field,
			fmt.Sprintf("%s must contain at most %d labels", field, WorkflowLabelMaxIDs),
		)
	}
	seenIDs := make(map[string]bool, len(labels))
	for index, label := range labels {
		if err := label.Validate(); err != nil {
			return prefixWorkflowLabelValidationField(field, index, err)
		}
		if seenIDs[label.ID] {
			return workflowRequestError(
				WorkflowRequestErrorInvalidValue,
				fmt.Sprintf("%s[%d].id", field, index),
				"label IDs must be unique",
			)
		}
		seenIDs[label.ID] = true
	}
	return nil
}

func prefixWorkflowLabelValidationField(field string, index int, err error) error {
	var validationErr WorkflowRequestValidationError
	if !errors.As(err, &validationErr) {
		return err
	}
	return workflowRequestError(
		validationErr.Code,
		fmt.Sprintf("%s[%d].%s", field, index, validationErr.Field),
		validationErr.Message,
	)
}

func validateLabelID(field string, id string) error {
	if _, err := runtimeids.ParseCanonicalUUIDv4(id, "label ID"); err != nil {
		return workflowRequestError(WorkflowRequestErrorInvalidValue, field, err.Error())
	}
	return nil
}

func validateLabelIDs(field string, ids []string) error {
	_, err := validateUniqueLabelIDs(field, ids)
	return err
}

func workflowLabelRPCValidationError(err error, projectID string, taskID string, nameIsInvalid bool) error {
	if err == nil {
		return nil
	}
	var validationErr WorkflowRequestValidationError
	if !errors.As(err, &validationErr) {
		return err
	}
	reason := WorkflowLabelErrorReasonInvalidMutation
	if nameIsInvalid && validationErr.Field == "name" && strings.TrimSpace(projectID) != "" {
		reason = WorkflowLabelErrorReasonInvalidName
	}
	var projectIDPointer *string
	if strings.TrimSpace(projectID) != "" {
		projectIDValue := projectID
		projectIDPointer = &projectIDValue
	}
	var taskIDPointer *string
	if strings.TrimSpace(taskID) != "" {
		taskIDValue := taskID
		taskIDPointer = &taskIDValue
	}
	field := validationErr.Field
	return &WorkflowLabelError{
		Reason:    reason,
		ProjectID: projectIDPointer,
		TaskID:    taskIDPointer,
		Field:     &field,
	}
}

func workflowLabelFilterRPCValidationError(err error) error {
	if err == nil {
		return nil
	}
	var validationErr WorkflowRequestValidationError
	if !errors.As(err, &validationErr) {
		return err
	}
	field := validationErr.Field
	return &WorkflowLabelError{
		Reason: WorkflowLabelErrorReasonInvalidFilter,
		Field:  &field,
	}
}
