package worktree

import (
	"errors"
	"strings"

	"core/shared/invariant"
	"core/shared/worktreecontract"
)

func validateCreateErrorBoundary(err error, operation string, policy invariant.Policy) error {
	contractErr := worktreecontract.ValidateCreateError(err, operation)
	if contractErr == nil {
		return nil
	}
	diagnostic := invariant.Diagnostic{
		Scope: invariant.ScopeWorktreeContract,
		Fields: map[invariant.Field]string{
			invariant.FieldOperation:       strings.TrimSpace(operation),
			invariant.FieldValidationCause: contractErr.Error(),
		},
	}
	var typedContract *worktreecontract.CreateContractError
	if errors.As(contractErr, &typedContract) && typedContract.Owner != nil {
		diagnostic.Fields[invariant.FieldRawOwner] = string(*typedContract.Owner)
	}
	policy.Check(false, diagnostic)
	return contractErr
}
