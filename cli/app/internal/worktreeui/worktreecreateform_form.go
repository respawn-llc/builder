package worktreeui

import "core/shared/worktreecontract"

type Field uint8

const (
	FieldBranchTarget Field = iota
	FieldBaseRef
	FieldActions
)

type CreateFormAction uint8

const (
	CreateFormActionCreate CreateFormAction = iota
	CreateFormActionCancel
)

func OrderedFields(kind worktreecontract.CreateTargetResolutionKind) []Field {
	fields := []Field{FieldBranchTarget}
	if kind == worktreecontract.CreateTargetResolutionKindNewBranch {
		fields = append(fields, FieldBaseRef)
	}
	fields = append(fields, FieldActions)
	return fields
}

func MoveField(field Field, kind worktreecontract.CreateTargetResolutionKind, delta int) Field {
	fields := OrderedFields(kind)
	index := 0
	for idx, candidate := range fields {
		if candidate == field {
			index = idx
			break
		}
	}
	index += delta
	if index < 0 {
		index = 0
	}
	if index >= len(fields) {
		index = len(fields) - 1
	}
	return fields[index]
}

func MoveCreateFormAction(action CreateFormAction, delta int) CreateFormAction {
	index := int(action) + delta
	if index < int(CreateFormActionCreate) {
		index = int(CreateFormActionCreate)
	}
	if index > int(CreateFormActionCancel) {
		index = int(CreateFormActionCancel)
	}
	return CreateFormAction(index)
}
