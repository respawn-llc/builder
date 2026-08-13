package migrationcheck

import (
	"fmt"
	"sort"
	"strings"
)

type ProjectionIssueCode string

const (
	IssueUnapprovedProjection         ProjectionIssueCode = "unapproved_projection"
	IssueUnexpectedProjectionIdentity ProjectionIssueCode = "unexpected_projection_identity"
	IssueProjectedIdentityAuthored    ProjectionIssueCode = "projected_identity_authored"
	IssueProjectionIdentityNotLegacy  ProjectionIssueCode = "projection_identity_not_legacy"
)

type ProjectionIssue struct {
	Code     ProjectionIssueCode
	Identity Identity
}

type ProjectionError struct {
	Issues []ProjectionIssue
}

func (e *ProjectionError) Error() string {
	var diagnostic strings.Builder
	fmt.Fprintf(&diagnostic, "finite projection failed with %d issue(s)", len(e.Issues))
	for _, issue := range e.Issues {
		fmt.Fprintf(&diagnostic, "\n- %s: %s", issue.Code, issue.Identity)
	}
	return diagnostic.String()
}

// CheckFiniteProjection compares a finite legacy identity fixture with its
// post-predecessor descriptor fixture. Only the exact approved projection may
// be absent; approved identities must remain absent rather than accidentally
// becoming part of the new contract.
func CheckFiniteProjection(legacy []Identity, descriptor []Identity, approved []Identity) error {
	return checkFiniteProjection(legacy, descriptor, approved, KENT554ProjectionIdentities())
}

// CheckKENT345FiniteProjection compares the pre-KENT-345 contract with the
// approved post-KENT-345 contract. Its omission set is deliberately separate
// from the KENT-554 capability-negotiation projection.
func CheckKENT345FiniteProjection(legacy []Identity, descriptor []Identity, approved []Identity) error {
	return checkFiniteProjection(legacy, descriptor, approved, KENT345ProjectionIdentities())
}

// CheckProjectSchemaFiniteProjection compares the legacy Project blocker
// contract with the client-worded descriptor contract.
func CheckProjectSchemaFiniteProjection(legacy []Identity, descriptor []Identity, approved []Identity) error {
	return checkFiniteProjection(legacy, descriptor, approved, ProjectSchemaProjectionIdentities())
}

func checkFiniteProjection(
	legacy []Identity,
	descriptor []Identity,
	approved []Identity,
	canonical []Identity,
) error {
	legacySet := identitySet(legacy)
	descriptorSet := identitySet(descriptor)
	canonicalProjection := identitySet(canonical)
	approvedSet := identitySet(approved)
	issues := make([]ProjectionIssue, 0)

	for identity := range approvedSet {
		if _, exists := canonicalProjection[identity]; !exists {
			issues = append(issues, ProjectionIssue{
				Code:     IssueUnexpectedProjectionIdentity,
				Identity: identity,
			})
		}
	}
	for identity := range canonicalProjection {
		if _, exists := approvedSet[identity]; !exists {
			issues = append(issues, ProjectionIssue{
				Code:     IssueUnexpectedProjectionIdentity,
				Identity: identity,
			})
		}
		if _, exists := legacySet[identity]; !exists {
			issues = append(issues, ProjectionIssue{
				Code:     IssueProjectionIdentityNotLegacy,
				Identity: identity,
			})
		}
		if _, exists := descriptorSet[identity]; exists {
			issues = append(issues, ProjectionIssue{
				Code:     IssueProjectedIdentityAuthored,
				Identity: identity,
			})
		}
	}
	for identity := range legacySet {
		if _, exists := descriptorSet[identity]; exists {
			continue
		}
		if _, exists := canonicalProjection[identity]; !exists {
			issues = append(issues, ProjectionIssue{
				Code:     IssueUnapprovedProjection,
				Identity: identity,
			})
		}
	}

	if len(issues) == 0 {
		return nil
	}
	sort.SliceStable(issues, func(left, right int) bool {
		if issues[left].Identity.String() != issues[right].Identity.String() {
			return issues[left].Identity.String() < issues[right].Identity.String()
		}
		return issues[left].Code < issues[right].Code
	})
	return &ProjectionError{Issues: issues}
}

func identitySet(identities []Identity) map[Identity]struct{} {
	result := make(map[Identity]struct{}, len(identities))
	for _, identity := range identities {
		result[identity] = struct{}{}
	}
	return result
}

// KENT554ProjectionIdentities is the complete approved protocol-negotiation
// deletion. CapabilityFacts and provider/model/import capability data are
// intentionally absent from this list.
func KENT554ProjectionIdentities() []Identity {
	identities := []Identity{
		fieldIdentity("core/shared/protocol", "HandshakeRequest", "ClientCapabilities"),
		typeIdentity("core/shared/protocol", "ClientCapabilities"),
		fieldIdentity("core/shared/protocol", "ClientCapabilities", "TranscriptLiveRunFinished"),
		fieldIdentity("core/shared/protocol", "ServerIdentity", "Capabilities"),
		typeIdentity("core/shared/protocol", "CapabilityFlags"),
	}
	for _, fieldName := range capabilityFlagFieldNames {
		identities = append(
			identities,
			fieldIdentity("core/shared/protocol", "CapabilityFlags", fieldName),
		)
	}
	return identities
}

// KENT345ProjectionIdentities is the complete approved deletion of generic
// application request identity. Queue Item, Worktree Setup Operation, Run,
// Agent Step, Session, Resource Generation, and transport-envelope correlation
// identities are intentionally absent from this list.
func KENT345ProjectionIdentities() []Identity {
	return []Identity{
		fieldIdentity("core/shared/serverapi", "ProcessKillRequest", "ClientRequestID"),
		fieldIdentity("core/shared/serverapi", "RunPromptRequest", "ClientRequestID"),
		fieldIdentity("core/shared/serverapi", "SessionPlanRequest", "ClientRequestID"),
		fieldIdentity("core/shared/serverapi", "SessionPersistInputDraftRequest", "ClientRequestID"),
		fieldIdentity("core/shared/serverapi", "SessionRetargetWorkspaceRequest", "ClientRequestID"),
		fieldIdentity("core/shared/serverapi", "SessionResolveTransitionRequest", "ClientRequestID"),
		fieldIdentity("core/shared/serverapi", "SessionRuntimeActivateRequest", "ClientRequestID"),
		fieldIdentity("core/shared/serverapi", "SessionRuntimeReleaseRequest", "ClientRequestID"),
		fieldIdentity("core/shared/serverapi", "RuntimeSetSessionNameRequest", "ClientRequestID"),
		fieldIdentity("core/shared/serverapi", "RuntimeSetThinkingLevelRequest", "ClientRequestID"),
		fieldIdentity("core/shared/serverapi", "RuntimeSetFastModeEnabledRequest", "ClientRequestID"),
		fieldIdentity("core/shared/serverapi", "RuntimeSetReviewerEnabledRequest", "ClientRequestID"),
		fieldIdentity("core/shared/serverapi", "RuntimeSetAutoCompactionEnabledRequest", "ClientRequestID"),
		fieldIdentity("core/shared/serverapi", "RuntimeSetQuestionsEnabledRequest", "ClientRequestID"),
		fieldIdentity("core/shared/serverapi", "RuntimeAppendCommittedEntryRequest", "ClientRequestID"),
		fieldIdentity("core/shared/serverapi", "RuntimeSubmitUserTurnRequest", "ClientRequestID"),
		fieldIdentity("core/shared/serverapi", "RuntimeSubmitUserShellCommandRequest", "ClientRequestID"),
		fieldIdentity("core/shared/serverapi", "RuntimeCompactContextRequest", "ClientRequestID"),
		fieldIdentity("core/shared/serverapi", "RuntimeInterruptRequest", "ClientRequestID"),
		fieldIdentity("core/shared/serverapi", "RuntimeLiveSteerRequest", "ClientRequestID"),
		fieldIdentity("core/shared/serverapi", "RuntimeLiveSteerResponse", "ClientRequestID"),
		fieldIdentity("core/shared/serverapi", "RuntimeLiveStopRequest", "ClientRequestID"),
		fieldIdentity("core/shared/serverapi", "RuntimeDiscardQueuedUserMessageRequest", "ClientRequestID"),
		fieldIdentity("core/shared/serverapi", "RuntimeRecordPromptHistoryRequest", "ClientRequestID"),
		fieldIdentity("core/shared/serverapi", "RuntimeGoalSetRequest", "ClientRequestID"),
		fieldIdentity("core/shared/serverapi", "RuntimeGoalStatusRequest", "ClientRequestID"),
		fieldIdentity("core/shared/serverapi", "RuntimeGoalClearRequest", "ClientRequestID"),

		fieldIdentity("core/shared/clientui", "QueuedUserMessage", "ClientRequestID"),
		fieldIdentity("core/shared/clientui", "RuntimeSubmitRequest", "ClientRequestID"),
		fieldIdentity("core/shared/clientui", "TranscriptQueuedMessageState", "ClientRequestID"),
		fieldIdentity("core/shared/clientui", "TranscriptUserMessageFlushed", "Messages"),
		typeIdentity("core/shared/clientui", "QueuedUserMessageIdentity"),
		fieldIdentity("core/shared/clientui", "QueuedUserMessageIdentity", "ClientRequestID"),
		fieldIdentity("core/shared/clientui", "QueuedUserMessageIdentity", "QueueItemID"),

		typeIdentity("core/shared/runtimeids", "RuntimeClientRequestID"),
		variableIdentity("core/shared/serverapi", "ErrClientRequestIDRequired"),
		functionIdentity("core/shared/serverapi", "validateClientRequestID"),
	}
}

// ProjectSchemaProjectionIdentities is the complete approved deletion of
// server-authored user-visible wording from Project blocker wire facts.
func ProjectSchemaProjectionIdentities() []Identity {
	return []Identity{
		fieldIdentity("core/shared/serverapi", "ProjectWorkspaceUnlinkBlocker", "Message"),
		fieldIdentity("core/shared/serverapi", "ProjectDeleteBlocker", "Message"),
	}
}

var capabilityFlagFieldNames = []string{
	"JSONRPCWebSocket",
	"AuthBootstrap",
	"ProjectAttach",
	"SessionAttach",
	"HealthEndpoint",
	"ReadinessEndpoint",
	"RunPrompt",
	"SessionPlan",
	"SessionLifecycle",
	"SessionTranscript",
	"SessionRuntime",
	"RuntimeControl",
	"RuntimeLiveControl",
	"PromptControl",
	"ProcessOutput",
	"AttentionNotifications",
	"OnboardingFinalize",
	"PromptCommands",
}
