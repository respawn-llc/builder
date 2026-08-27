package protoapi

import (
	"errors"
	"strings"

	"buf.build/go/protovalidate"
	"core/shared/invariant"
	"core/shared/worktreecontract"
)

const (
	worktreeCreateNewBranchBranchNameRule = "kent.worktree.create_spec.new_branch_branch_name_required"
	worktreeCreateNewBranchBaseRefRule    = "kent.worktree.create_spec.new_branch_base_ref_required"
	worktreeCreateExistingBaseRefRule     = "kent.worktree.create_spec.existing_target_base_ref_required"
	worktreeCreateExistingBranchRule      = "kent.worktree.create_spec.existing_target_branch_name_forbidden"
)

func ClassifyWorktreeCreateValidation(err error) error {
	var validationErr *protovalidate.ValidationError
	if !errors.As(err, &validationErr) || validationErr == nil {
		return err
	}
	violation := selectWorktreeCreateViolation(validationErr.Violations)
	if violation == nil || violation.Proto == nil {
		return err
	}
	owner := worktreecontract.CreateErrorOwnerForm
	if violation.Proto.GetRuleId() == worktreeCreateNewBranchBaseRefRule {
		owner = worktreecontract.CreateErrorOwnerBaseRef
	}
	return worktreecontract.NewCreateError(owner, violation.Proto.GetMessage(), err)
}

func ValidateWorktreeCreateErrorBoundary(err error, operation string, policy invariant.Policy) error {
	contractErr := worktreecontract.ValidateCreateError(err, operation)
	if contractErr == nil {
		return nil
	}
	fields := map[invariant.Field]string{
		invariant.FieldOperation:       strings.TrimSpace(operation),
		invariant.FieldValidationCause: contractErr.Error(),
	}
	var typed *worktreecontract.CreateContractError
	if errors.As(contractErr, &typed) && typed.Owner != nil {
		fields[invariant.FieldRawOwner] = string(*typed.Owner)
	}
	policy.Check(false, invariant.Diagnostic{Scope: invariant.ScopeWorktreeContract, Fields: fields})
	return contractErr
}

func selectWorktreeCreateViolation(violations []*protovalidate.Violation) *protovalidate.Violation {
	for _, fieldNumber := range []int32{1, 2} {
		for _, violation := range violations {
			if worktreeCreateTopLevelField(violation) == fieldNumber {
				return violation
			}
		}
	}
	for _, ruleID := range []string{
		worktreeCreateNewBranchBranchNameRule,
		worktreeCreateNewBranchBaseRefRule,
		worktreeCreateExistingBaseRefRule,
		worktreeCreateExistingBranchRule,
	} {
		for _, violation := range violations {
			if violation != nil && violation.Proto != nil && violation.Proto.GetRuleId() == ruleID {
				return violation
			}
		}
	}
	for _, violation := range violations {
		if violation != nil && violation.Proto != nil {
			return violation
		}
	}
	return nil
}

func worktreeCreateTopLevelField(violation *protovalidate.Violation) int32 {
	if violation == nil || violation.Proto == nil {
		return 0
	}
	elements := violation.Proto.GetField().GetElements()
	if len(elements) == 0 {
		return 0
	}
	return elements[0].GetFieldNumber()
}
