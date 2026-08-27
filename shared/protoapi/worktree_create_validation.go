package protoapi

import (
	"errors"

	"buf.build/go/protovalidate"
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
