package core_test

import (
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"core/internal/testharness/testsetup"
)

type goListPackage struct {
	ImportPath string
	Imports    []string
}

func TestArchitectureBoundaries(t *testing.T) {
	repoRoot := findRepoRoot(t)
	packages := loadRepoPackages(t, repoRoot)
	violations := make([]string, 0)
	for _, pkg := range packages {
		importPath := strings.TrimSpace(pkg.ImportPath)
		if importPath == "" {
			continue
		}
		for _, imported := range pkg.Imports {
			trimmedImport := strings.TrimSpace(imported)
			if trimmedImport == "" || !strings.HasPrefix(trimmedImport, "core/") {
				continue
			}
			switch {
			case strings.HasPrefix(importPath, "core/server/") && strings.HasPrefix(trimmedImport, "core/cli/"):
				violations = append(violations, importPath+" must not import frontend package "+trimmedImport)
			case strings.HasPrefix(importPath, "core/shared/") && strings.HasPrefix(trimmedImport, "core/cli/"):
				violations = append(violations, importPath+" must not import frontend package "+trimmedImport)
			case strings.HasPrefix(importPath, "core/shared/") && strings.HasPrefix(trimmedImport, "core/server/"):
				violations = append(violations, importPath+" must not import server package "+trimmedImport)
			case strings.HasPrefix(importPath, "core/cli/") && trimmedImport == "core/server/metadata":
				violations = append(violations, importPath+" must not import persistence metadata package "+trimmedImport)
			}
		}
	}
	if len(violations) > 0 {
		t.Fatalf("architecture boundary violations:\n%s", strings.Join(violations, "\n"))
	}
}

func TestSharedClientUIRemainsDTOOnly(t *testing.T) {
	repoRoot := findRepoRoot(t)
	clientUIRoot := filepath.Join(repoRoot, "shared", "clientui")
	allowedTypes := map[string]struct{}{
		"ApprovalDecision":                                 {},
		"ApprovalOption":                                   {},
		"ApprovalPromptAnswer":                             {},
		"AssistantStreamAbortReason":                       {},
		"AttentionNotification":                            {},
		"AttentionNotificationApprovalState":               {},
		"AttentionNotificationEvent":                       {},
		"AttentionNotificationEventType":                   {},
		"AttentionNotificationFocusKind":                   {},
		"AttentionNotificationID":                          {},
		"AttentionNotificationInterruptedCurrentNodeState": {},
		"AttentionNotificationKind":                        {},
		"AttentionNotificationQuestionState":               {},
		"AttentionNotificationSource":                      {},
		"AttentionNotificationTarget":                      {},
		"AttentionNotificationTargetKind":                  {},
		"AttentionNotificationTaskDetailFocus":             {},
		"AttentionNotificationWorkflowApprovalState":       {},
		"BackgroundLifecycle":                              {},
		"BackgroundProcess":                                {},
		"CompactionLifecycle":                              {},
		"CompactionState":                                  {},
		"ConversationFreshness":                            {},
		"Goal":                                             {},
		"GoalAvailability":                                 {},
		"GoalEnvelope":                                     {},
		"EntryVisibility":                                  {},
		"LiveRunNoFinalReason":                             {},
		"LiveRunResultKind":                                {},
		"LiveRunStatus":                                    {},
		"MessagePhase":                                     {},
		"MessageType":                                      {},
		"NoticeID":                                         {},
		"OperationalDiagnosticCode":                        {},
		"PendingApproval":                                  {},
		"PendingAsk":                                       {},
		"ProcessClient":                                    {},
		"ProcessID":                                        {},
		"ProcessOutputChunk":                               {},
		"ProjectAvailability":                              {},
		"ProjectOverview":                                  {},
		"ProjectSummary":                                   {},
		"ProjectWorkspaceSummary":                          {},
		"PromptAnswer":                                     {},
		"PromptID":                                         {},
		"QueuedUserMessage":                                {},
		"QueuedUserMessageFailureReason":                   {},
		"QueuedUserMessageIdentity":                        {},
		"QueuedUserMessageStatus":                          {},
		"ReadModelVersion":                                 {},
		"ReviewerLifecycle":                                {},
		"ReviewerState":                                    {},
		"RunLifecycle":                                     {},
		"RunLifecyclePhase":                                {},
		"RunMode":                                          {},
		"RunStatus":                                        {},
		"RuntimeActiveStep":                                {},
		"RuntimeActivity":                                  {},
		"RuntimeActivityActiveKind":                        {},
		"RuntimeActivityState":                             {},
		"RuntimeClient":                                    {},
		"RuntimeCompactRequest":                            {},
		"RuntimeConnectionLifecycle":                       {},
		"RuntimeContextUsage":                              {},
		"RuntimeGoal":                                      {},
		"RuntimeGoalStatus":                                {},
		"RuntimeMainView":                                  {},
		"RuntimeReadModelUpdate":                           {},
		"RuntimeSessionView":                               {},
		"RuntimeShellRequest":                              {},
		"RuntimeStatus":                                    {},
		"RuntimeSubmitRequest":                             {},
		"SessionExecutionTarget":                           {},
		"SessionExecutionWorktreeTarget":                   {},
		"SessionSummary":                                   {},
		"StepLifecycleState":                               {},
		"ToolAbortReason":                                  {},
		"ToolCallID":                                       {},
		"ToolProvenance":                                   {},
		"TranscriptAssistantDelta":                         {},
		"TranscriptAssistantRow":                           {},
		"TranscriptAssistantStream":                        {},
		"TranscriptAssistantStreamAbort":                   {},
		"TranscriptBackgroundActivity":                     {},
		"TranscriptBackgroundNoticeIdentity":               {},
		"TranscriptCacheWarning":                           {},
		"TranscriptCommittedRow":                           {},
		"TranscriptCompactionNotice":                       {},
		"TranscriptCompactionStatus":                       {},
		"TranscriptContextUsage":                           {},
		"TranscriptDiagnostic":                             {},
		"TranscriptDiagnosticCode":                         {},
		"TranscriptGoal":                                   {},
		"TranscriptGoalStatus":                             {},
		"TranscriptHydration":                              {},
		"TranscriptLiveRunResult":                          {},
		"TranscriptMessage":                                {},
		"TranscriptMessageKind":                            {},
		"TranscriptEvent":                                  {},
		"TranscriptEventPayload":                           {},
		"transcriptEventPayload":                           {},
		"transcriptEventPayloadValue":                      {},
		"TranscriptMessageType":                            {},
		"TranscriptNoticeReason":                           {},
		"TranscriptNoticeRow":                              {},
		"TranscriptNoticeSeverity":                         {},
		"TranscriptOperationalDiagnostic":                  {},
		"TranscriptPage":                                   {},
		"TranscriptPageRequest":                            {},
		"TranscriptPrompt":                                 {},
		"TranscriptPromptKind":                             {},
		"TranscriptPromptStatus":                           {},
		"TranscriptQueuedMessageState":                     {},
		"TranscriptReasoningTraceReset":                    {},
		"TranscriptReasoningTraceUpdate":                   {},
		"TranscriptThinkingStatusUpdate":                   {},
		"TranscriptReasoningTraceRow":                      {},
		"TranscriptProviderReasoningTraceIdentity":         {},
		"TranscriptReasoningTraceIdentity":                 {},
		"TranscriptReviewerFeedbackRow":                    {},
		"TranscriptReviewerErrorRow":                       {},
		"TranscriptReviewerState":                          {},
		"TranscriptRowKind":                                {},
		"TranscriptSessionIdentity":                        {},
		"TranscriptSessionStatus":                          {},
		"TranscriptStepState":                              {},
		"TranscriptToolAbort":                              {},
		"TranscriptToolRow":                                {},
		"TranscriptToolStart":                              {},
		"TranscriptUserMessageFlushed":                     {},
		"TranscriptUserRow":                                {},
		"TranscriptWorkflowSession":                        {},
		"TranscriptWorktreeContext":                        {},
		"TranscriptWorktreeTransitionOutcome":              {},
		"UserTurnResultKind":                               {},
		"UserTurnSubmission":                               {},
		"WorkflowSessionStatus":                            {},
		"WorktreeTransitionFailure":                        {},
		"WorktreeTransitionID":                             {},
		"WorktreeTransitionKind":                           {},
		"WorktreeTransitionOutcome":                        {},
		"WorktreeTransitionState":                          {},
		"WorktreeDirtyState":                               {},
		"WorktreeDirtyStateKind":                           {},
	}
	allowedFuncs := map[string]struct{}{
		"NewTranscriptEvent":                                      {},
		"TranscriptEvent.IsZero":                                  {},
		"TranscriptEvent.Payload":                                 {},
		"TranscriptEvent.Kind":                                    {},
		"TranscriptEvent.Validate":                                {},
		"NewTranscriptMessage":                                    {},
		"TranscriptMessage.Event":                                 {},
		"TranscriptMessage.Payload":                               {},
		"TranscriptMessage.Kind":                                  {},
		"TranscriptMessage.MarshalJSON":                           {},
		"TranscriptMessage.UnmarshalJSON":                         {},
		"unmarshalTranscriptEvent":                                {},
		"decodeTranscriptPayload":                                 {},
		"CompactionLifecycle.IsRunning":                           {},
		"ConversationFreshness.IsFresh":                           {},
		"IdleRunLifecycle":                                        {},
		"MustRunLifecycle":                                        {},
		"NewCompactionLifecycle":                                  {},
		"NewReadModelVersion":                                     {},
		"NewReviewerLifecycle":                                    {},
		"NewRunLifecycle":                                         {},
		"NewRuntimeConnectionLifecycle":                           {},
		"NewWorktreeTransitionID":                                 {},
		"NormalizeSessionExecutionTarget":                         {},
		"SessionExecutionWorkspaceRoot":                           {},
		"NormalizeThinkingLevel":                                  {},
		"ParseWorktreeTransitionID":                               {},
		"PromptID.Validate":                                       {},
		"ReadModelVersion.NewerThan":                              {},
		"ReadModelVersion.Validate":                               {},
		"ReviewerLifecycle.IsBlocking":                            {},
		"ReviewerLifecycle.IsRunning":                             {},
		"ReviewerLifecycle.Validate":                              {},
		"RunLifecycle.IsFinished":                                 {},
		"RunLifecycle.IsGoalLoopRunning":                          {},
		"RunLifecycle.IsRunning":                                  {},
		"RunLifecycle.Validate":                                   {},
		"RuntimeActiveStep.Validate":                              {},
		"RuntimeActivity.ActiveForControl":                        {},
		"RuntimeActivity.Validate":                                {},
		"RuntimeActivityActiveKind.Validate":                      {},
		"RuntimeCompactRequest.Validate":                          {},
		"RuntimeConnectionLifecycle.IsDisconnected":               {},
		"RuntimeReadModelUpdate.Validate":                         {},
		"RuntimeShellRequest.Validate":                            {},
		"RuntimeSubmitRequest.Validate":                           {},
		"Goal.Validate":                                           {},
		"GoalAvailability.Validate":                               {},
		"GoalEnvelope.Validate":                                   {},
		"ProjectGoal":                                             {},
		"RuntimeGoalFromEnvelope":                                 {},
		"TranscriptGoalStatusFromEnvelope":                        {},
		"SessionExecutionTargetIsZero":                            {},
		"SessionExecutionTargetsEqual":                            {},
		"ToolCallID.Validate":                                     {},
		"ToolProvenance.Validate":                                 {},
		"TranscriptAssistantDelta.Validate":                       {},
		"TranscriptAssistantRow.Validate":                         {},
		"TranscriptAssistantStream.Validate":                      {},
		"TranscriptAssistantStreamAbort.Validate":                 {},
		"TranscriptBackgroundActivity.Validate":                   {},
		"TranscriptBackgroundNoticeIdentity.Validate":             {},
		"TranscriptCacheWarning.Validate":                         {},
		"TranscriptCommittedRow.Validate":                         {},
		"TranscriptCompactionNotice.Validate":                     {},
		"TranscriptCompactionStatus.Validate":                     {},
		"TranscriptContextUsage.Validate":                         {},
		"TranscriptDiagnostic.Validate":                           {},
		"TranscriptGoalStatus.Validate":                           {},
		"TranscriptHydration.Validate":                            {},
		"TranscriptHydration.validateActiveOwnership":             {},
		"TranscriptLiveRunResult.Validate":                        {},
		"TranscriptMessage.Validate":                              {},
		"TranscriptMessage.ValidatePayload":                       {},
		"TranscriptHydration.transcriptEventKind":                 {},
		"TranscriptCommittedRow.transcriptEventKind":              {},
		"TranscriptAssistantDelta.transcriptEventKind":            {},
		"TranscriptAssistantStreamAbort.transcriptEventKind":      {},
		"TranscriptReasoningTraceUpdate.transcriptEventKind":      {},
		"TranscriptReasoningTraceReset.transcriptEventKind":       {},
		"TranscriptThinkingStatusUpdate.transcriptEventKind":      {},
		"TranscriptToolStart.transcriptEventKind":                 {},
		"TranscriptToolAbort.transcriptEventKind":                 {},
		"TranscriptUserMessageFlushed.transcriptEventKind":        {},
		"TranscriptQueuedMessageState.transcriptEventKind":        {},
		"TranscriptStepState.transcriptEventKind":                 {},
		"TranscriptReviewerState.transcriptEventKind":             {},
		"RuntimeReadModelUpdate.transcriptEventKind":              {},
		"TranscriptSessionStatus.transcriptEventKind":             {},
		"TranscriptSessionIdentity.transcriptEventKind":           {},
		"TranscriptCompactionStatus.transcriptEventKind":          {},
		"TranscriptContextUsage.transcriptEventKind":              {},
		"TranscriptGoalStatus.transcriptEventKind":                {},
		"TranscriptBackgroundActivity.transcriptEventKind":        {},
		"TranscriptPrompt.transcriptEventKind":                    {},
		"TranscriptWorktreeTransitionOutcome.transcriptEventKind": {},
		"TranscriptOperationalDiagnostic.transcriptEventKind":     {},
		"TranscriptLiveRunResult.transcriptEventKind":             {},
		"TranscriptMessageType.Validate":                          {},
		"TranscriptNoticeRow.Validate":                            {},
		"TranscriptOperationalDiagnostic.Validate":                {},
		"TranscriptPrompt.Validate":                               {},
		"TranscriptPrompt.validateApproval":                       {},
		"TranscriptPrompt.validateQuestion":                       {},
		"TranscriptPromptKind.Validate":                           {},
		"TranscriptPromptStatus.Validate":                         {},
		"TranscriptQueuedMessageState.Validate":                   {},
		"TranscriptReasoningTraceReset.Validate":                  {},
		"TranscriptReasoningTraceUpdate.Validate":                 {},
		"TranscriptThinkingStatusUpdate.Validate":                 {},
		"TranscriptReasoningTraceRow.Validate":                    {},
		"TranscriptProviderReasoningTraceIdentity.Validate":       {},
		"TranscriptReasoningTraceIdentity.Validate":               {},
		"TranscriptReasoningTraceIdentity.String":                 {},
		"TranscriptReviewerFeedbackRow.Validate":                  {},
		"TranscriptReviewerErrorRow.Validate":                     {},
		"TranscriptReviewerState.Validate":                        {},
		"TranscriptSessionIdentity.Validate":                      {},
		"TranscriptSessionStatus.Validate":                        {},
		"TranscriptStepState.Validate":                            {},
		"TranscriptToolAbort.Validate":                            {},
		"TranscriptToolRow.Validate":                              {},
		"TranscriptToolStart.Validate":                            {},
		"TranscriptUserMessageFlushed.Validate":                   {},
		"TranscriptUserRow.Validate":                              {},
		"TranscriptWorkflowSession.Validate":                      {},
		"TranscriptWorktreeContext.Validate":                      {},
		"TranscriptWorktreeTransitionOutcome.Validate":            {},
		"WorktreeTransitionID.MarshalJSON":                        {},
		"WorktreeTransitionID.String":                             {},
		"WorktreeTransitionID.UnmarshalJSON":                      {},
		"WorktreeTransitionID.Validate":                           {},
		"WorktreeTransitionOutcome.Validate":                      {},
		"validateHydrationBackgrounds":                            {},
		"validateHydrationPrompts":                                {},
		"validateHydrationQueuedMessages":                         {},
		"validateHydrationStepOwner":                              {},
		"validateHydrationTools":                                  {},
		"validateOptionalNonEmptyString":                          {},
		"validatePresentSessionExecutionTarget":                   {},
		"validateRequiredOptionalText":                            {},
		"validateSessionExecutionWorktree":                        {},
		"TranscriptCommittedRow.ValidateStructure":                {},
	}
	seenTypes := make(map[string]struct{}, len(allowedTypes))
	seenFuncs := make(map[string]struct{}, len(allowedFuncs))
	violations := make([]string, 0)
	if err := filepath.WalkDir(clientUIRoot, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		fileSet := token.NewFileSet()
		file, parseErr := parser.ParseFile(fileSet, path, nil, parser.SkipObjectResolution)
		if parseErr != nil {
			return parseErr
		}
		relPath, relErr := filepath.Rel(repoRoot, path)
		if relErr != nil {
			relPath = path
		}
		for _, decl := range file.Decls {
			switch typedDecl := decl.(type) {
			case *ast.FuncDecl:
				name := funcDeclBoundaryName(typedDecl)
				if _, allowed := allowedFuncs[name]; allowed {
					seenFuncs[name] = struct{}{}
					continue
				}
				if funcUsesRuntimeEventPolicyType(typedDecl.Type) {
					violations = append(violations, relPath+": DTO-only package must not define runtime-event policy helper "+name)
					continue
				}
				violations = append(violations, relPath+": DTO-only package added function "+name+" without DTO-boundary review")
			case *ast.GenDecl:
				if typedDecl.Tok != token.TYPE {
					continue
				}
				for _, spec := range typedDecl.Specs {
					typeSpec, ok := spec.(*ast.TypeSpec)
					if !ok {
						continue
					}
					name := typeSpec.Name.Name
					if _, allowed := allowedTypes[name]; allowed {
						seenTypes[name] = struct{}{}
					} else {
						violations = append(violations, relPath+": DTO-only package added type "+name+" without DTO-boundary review")
					}
				}
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("scan shared clientui DTO boundaries: %v", err)
	}
	for name := range allowedTypes {
		if _, found := seenTypes[name]; !found {
			violations = append(violations, "remove stale shared/clientui DTO type allowance "+name)
		}
	}
	for name := range allowedFuncs {
		if _, found := seenFuncs[name]; !found {
			violations = append(violations, "remove stale shared/clientui DTO function allowance "+name)
		}
	}
	if len(violations) > 0 {
		t.Fatalf("shared/clientui DTO boundary violations:\n%s", strings.Join(violations, "\n"))
	}
}

func TestSharedClientUIHasNoOldRuntimeLivenessDTOs(t *testing.T) {
	repoRoot := findRepoRoot(t)
	clientUIRoot := filepath.Join(repoRoot, "shared", "clientui")
	forbidden := map[string]struct{}{
		"ActiveRun":                  {},
		"ExternalRuntime":            {},
		"ExternalRuntimeState":       {},
		"ExternalRuntimeStatus":      {},
		"EventExternalRuntimeStatus": {},
		"RunView":                    {},
	}
	var violations []string
	if err := filepath.WalkDir(clientUIRoot, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		relPath, relErr := filepath.Rel(repoRoot, path)
		if relErr != nil {
			relPath = path
		}
		fileSet := token.NewFileSet()
		file, parseErr := parser.ParseFile(fileSet, path, nil, parser.SkipObjectResolution)
		if parseErr != nil {
			return parseErr
		}
		ast.Inspect(file, func(node ast.Node) bool {
			ident, ok := node.(*ast.Ident)
			if !ok {
				return true
			}
			if _, found := forbidden[ident.Name]; !found {
				return true
			}
			position := fileSet.Position(ident.Pos())
			violations = append(violations, relPath+":"+position.String()+": old client-facing runtime liveness DTO "+ident.Name+" must stay deleted")
			return true
		})
		return nil
	}); err != nil {
		t.Fatalf("scan shared/clientui liveness DTOs: %v", err)
	}
	if len(violations) > 0 {
		t.Fatalf("old client-facing runtime liveness DTOs restored:\n%s", strings.Join(violations, "\n"))
	}
}

func funcDeclBoundaryName(fn *ast.FuncDecl) string {
	if fn == nil || fn.Recv == nil || len(fn.Recv.List) == 0 {
		if fn == nil {
			return ""
		}
		return fn.Name.Name
	}
	return receiverTypeName(fn.Recv.List[0].Type) + "." + fn.Name.Name
}

func receiverTypeName(expr ast.Expr) string {
	switch typedExpr := expr.(type) {
	case *ast.Ident:
		return typedExpr.Name
	case *ast.StarExpr:
		return receiverTypeName(typedExpr.X)
	default:
		return ""
	}
}

func funcUsesRuntimeEventPolicyType(funcType *ast.FuncType) bool {
	if funcType == nil {
		return false
	}
	for _, fields := range []*ast.FieldList{funcType.Params, funcType.Results} {
		if fields == nil {
			continue
		}
		for _, field := range fields.List {
			if exprUsesRuntimeEventPolicyType(field.Type) {
				return true
			}
		}
	}
	return false
}

func exprUsesRuntimeEventPolicyType(expr ast.Expr) bool {
	switch typedExpr := expr.(type) {
	case *ast.Ident:
		switch typedExpr.Name {
		case "Event", "ReasoningDelta", "BackgroundShellEvent":
			return true
		default:
			return false
		}
	case *ast.StarExpr:
		return exprUsesRuntimeEventPolicyType(typedExpr.X)
	case *ast.ArrayType:
		return exprUsesRuntimeEventPolicyType(typedExpr.Elt)
	case *ast.MapType:
		return exprUsesRuntimeEventPolicyType(typedExpr.Key) || exprUsesRuntimeEventPolicyType(typedExpr.Value)
	default:
		return false
	}
}

func TestCLIPackagesDoNotImportServerOutsideCompositionBridges(t *testing.T) {
	repoRoot := findRepoRoot(t)
	// Keep CLI -> server imports concentrated in documented local composition
	// seams. UI, TUI, status, and command handlers must use shared contracts.
	// Every exception below is an exact file/import pair introduced by deleting
	// a one-line bridge; new server imports still fail by default.
	allowedServerImportsByFile := allowedCLIServerImports()
	actualAllowedServerImportsByFile := make(map[string]map[string]struct{})
	violations := make([]string, 0)
	walkRepositoryGoSources(t, repoRoot, repositoryGoSourceScan{
		Operation:    "scan cli server imports",
		Root:         "cli",
		Recursive:    true,
		IncludeTests: false,
		Mode:         parser.ImportsOnly,
		Selection:    allRepositoryGoSources{},
	}, func(source parsedGoSource) {
		for _, spec := range source.File.Imports {
			importPath := strings.Trim(spec.Path.Value, "\"")
			if !strings.HasPrefix(importPath, "core/server/") {
				continue
			}
			if importPath == "core/server/subagentpolicy" {
				violations = append(violations, source.RelPath+": CLI must not own or execute server subagent launch authorization")
				continue
			}
			allowedImports := allowedServerImportsByFile[source.RelPath]
			if _, allowed := allowedImports[importPath]; allowed {
				if actualAllowedServerImportsByFile[source.RelPath] == nil {
					actualAllowedServerImportsByFile[source.RelPath] = make(map[string]struct{})
				}
				actualAllowedServerImportsByFile[source.RelPath][importPath] = struct{}{}
				continue
			}
			violations = append(violations, source.RelPath+": CLI production file must not import server package "+importPath)
		}
	})
	for relPath, expectedImports := range allowedServerImportsByFile {
		actualImports := actualAllowedServerImportsByFile[relPath]
		for importPath, reason := range expectedImports {
			if strings.TrimSpace(reason) == "" {
				violations = append(violations, relPath+": allowed server import "+importPath+" must include rationale text")
			}
			if _, found := actualImports[importPath]; !found {
				violations = append(violations, relPath+": remove stale allowed server import "+importPath+" from architecture test")
			}
		}
	}
	if len(violations) > 0 {
		t.Fatalf("cli to server import boundary violations:\n%s", strings.Join(violations, "\n"))
	}
}

func allowedCLIServerImports() map[string]map[string]string {
	return map[string]map[string]string{
		filepath.Join("cli", "app", "auth_gate.go"): {
			"core/server/auth": "remote auth collection uses provider OAuth primitives at the client boundary",
		},
		filepath.Join("cli", "app", "remote_auth_bootstrap.go"): {
			"core/server/auth": "remote auth bootstrap constructs server auth grants at the startup boundary",
		},
		filepath.Join("cli", "app", "ui_layout_rendering_status.go"): {
			"core/server/llm": "status line uses the server-owned model display label after deleting the app bridge package",
		},
		filepath.Join("cli", "app", "internal", "status", "statuscollect_model.go"): {
			"core/server/llm": "status collection uses the server-owned model display label after deleting the app bridge package",
		},
		filepath.Join("cli", "app", "internal", "status", "statuscollect_collect.go"): {
			"core/server/runtime": "status collection reads runtime memory status at the CLI status boundary",
		},
		filepath.Join("cli", "app", "internal", "status", "statuscollect_environment.go"): {
			"core/server/runtime": "status collection reads runtime memory status at the CLI status boundary",
		},
		filepath.Join("cli", "app", "internal", "authui", "oauthadapter_oauth.go"): {
			"core/server/auth": "OAuth adapter re-exports server auth OAuth DTO aliases",
		},
		filepath.Join("cli", "app", "internal", "startupconfig", "config.go"): {
			"core/server/bootstrap": "startup config resolves server bootstrap context at the startup boundary",
		},
		filepath.Join("cli", "kent", "serve.go"): {
			"core/server/startup": "kent serve command is a composition root",
		},
	}
}

func TestCLIAppUIFilesDoNotAddServerImports(t *testing.T) {
	repoRoot := findRepoRoot(t)
	violations := make([]string, 0)
	walkRepositoryGoSources(t, repoRoot, repositoryGoSourceScan{
		Operation:    "scan cli app UI sources",
		Root:         filepath.Join("cli", "app"),
		Recursive:    false,
		IncludeTests: false,
		Mode:         parser.ImportsOnly,
		Selection:    repositoryGoSourceBasePrefix{Prefix: "ui"},
	}, func(source parsedGoSource) {
		for _, spec := range source.File.Imports {
			importPath := strings.Trim(spec.Path.Value, "\"")
			if !strings.HasPrefix(importPath, "core/server/") {
				continue
			}
			if !isAllowedCLIAppRootServerImport(source.RelPath, importPath) {
				violations = append(violations, source.RelPath+": UI file must not add server import "+importPath)
			}
		}
	})
	if len(violations) > 0 {
		t.Fatalf("cli app UI server import boundary violations:\n%s", strings.Join(violations, "\n"))
	}
}

func TestCLIUIFilesDoNotBypassServerAttachService(t *testing.T) {
	repoRoot := findRepoRoot(t)
	violations := make([]string, 0)
	checkImports := func(source parsedGoSource) {
		for _, spec := range source.File.Imports {
			importPath := strings.Trim(spec.Path.Value, "\"")
			if importPath == "core/cli/app/internal/serverattach" || importPath == "core/cli/app/internal/remoteattach" {
				violations = append(violations, source.RelPath+": UI files must not import startup attachment package "+importPath)
			}
		}
	}
	walkRepositoryGoSources(t, repoRoot, repositoryGoSourceScan{
		Operation:    "scan UI sources under " + filepath.Join(repoRoot, "cli", "app"),
		Root:         filepath.Join("cli", "app"),
		Recursive:    false,
		IncludeTests: false,
		Mode:         parser.ImportsOnly,
		Selection:    repositoryGoSourceBasePrefix{Prefix: "ui"},
	}, checkImports)
	walkRepositoryGoSources(t, repoRoot, repositoryGoSourceScan{
		Operation:    "scan UI sources under " + filepath.Join(repoRoot, "cli", "tui"),
		Root:         filepath.Join("cli", "tui"),
		Recursive:    true,
		IncludeTests: false,
		Mode:         parser.ImportsOnly,
		Selection:    allRepositoryGoSources{},
	}, checkImports)
	if len(violations) > 0 {
		t.Fatalf("cli UI server attach bypass violations:\n%s", strings.Join(violations, "\n"))
	}
}

func TestCLIAppRootFilesDoNotImportServerPackages(t *testing.T) {
	repoRoot := findRepoRoot(t)
	violations := make([]string, 0)
	walkRepositoryGoSources(t, repoRoot, repositoryGoSourceScan{
		Operation:    "scan cli app root sources",
		Root:         filepath.Join("cli", "app"),
		Recursive:    false,
		IncludeTests: false,
		Mode:         parser.ImportsOnly,
		Selection:    allRepositoryGoSources{},
	}, func(source parsedGoSource) {
		for _, spec := range source.File.Imports {
			importPath := strings.Trim(spec.Path.Value, "\"")
			if strings.HasPrefix(importPath, "core/server/") && !isAllowedCLIAppRootServerImport(source.RelPath, importPath) {
				violations = append(violations, source.RelPath+": app root package must not import server package "+importPath)
			}
		}
	})
	if len(violations) > 0 {
		t.Fatalf("cli app root server import boundary violations:\n%s", strings.Join(violations, "\n"))
	}
}

func TestCLIAppDoesNotReintroduceEmbeddedServerServiceLocator(t *testing.T) {
	repoRoot := findRepoRoot(t)
	violations := make([]string, 0)
	walkRepositoryGoSources(t, repoRoot, repositoryGoSourceScan{
		Operation:    "scan cli app root sources",
		Root:         filepath.Join("cli", "app"),
		Recursive:    false,
		IncludeTests: true,
		Mode:         parser.SkipObjectResolution,
		Selection:    allRepositoryGoSources{},
	}, func(source parsedGoSource) {
		ast.Inspect(source.File, func(node ast.Node) bool {
			ident, ok := node.(*ast.Ident)
			if ok && ident.Name == "embeddedServer" {
				violations = append(violations, source.RelPath+": must not reintroduce embeddedServer service-locator identifier")
			}
			return true
		})
	})
	if len(violations) > 0 {
		t.Fatalf("cli app embeddedServer service-locator violations:\n%s", strings.Join(violations, "\n"))
	}
}

func TestCLIAppStartupEntrypointsUseServerAttach(t *testing.T) {
	repoRoot := findRepoRoot(t)
	runPath := filepath.Join("cli", "app", "run_prompt_target.go")
	sessionPath := filepath.Join("cli", "app", "session_server_target.go")
	serverAttachDir := filepath.Join("cli", "app", "internal", "serverattach")
	violations := make([]string, 0)
	walkRepositoryGoSources(t, repoRoot, repositoryGoSourceScan{
		Operation:    "scan cli app server attachment topology",
		Root:         filepath.Join("cli", "app"),
		Recursive:    true,
		IncludeTests: false,
		Mode:         parser.ImportsOnly,
		Selection:    allRepositoryGoSources{},
	}, func(source parsedGoSource) {
		for _, spec := range source.File.Imports {
			switch strings.Trim(spec.Path.Value, "\"") {
			case "core/cli/app/internal/serverattach":
				if source.RelPath != runPath {
					violations = append(violations, source.RelPath+": only run_prompt_target.go may import serverattach")
				}
			case "core/cli/app/internal/daemonlaunch", "core/cli/app/internal/embeddedattach":
				if filepath.Dir(source.RelPath) == serverAttachDir {
					violations = append(violations, source.RelPath+": attach-only package must not import daemon or embedded startup")
				}
			case "core/cli/app/internal/targetstartup", "core/cli/app/internal/targetresolve":
				violations = append(violations, source.RelPath+": startup entrypoint must not import a legacy startup package")
			}
		}
	})
	for _, relPath := range []string{sessionPath, runPath} {
		file, err := parser.ParseFile(token.NewFileSet(), filepath.Join(repoRoot, relPath), nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("parse %s: %v", relPath, err)
		}
		usesAttachRunPrompt, usesConfiguredDial := false, false
		ast.Inspect(file, func(node ast.Node) bool {
			selector, ok := node.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			ident, ok := selector.X.(*ast.Ident)
			if !ok {
				return true
			}
			if ident.Name == "serverattach" {
				switch selector.Sel.Name {
				case "AttachRunPrompt":
					usesAttachRunPrompt = true
				case "Resolve":
					violations = append(violations, relPath+": startup entrypoint must not reintroduce generic serverattach.Resolve")
				}
			}
			if ident.Name == "client" && selector.Sel.Name == "DialConfiguredRemote" {
				usesConfiguredDial = true
			}
			if ident.Name == "remoteattach" && (selector.Sel.Name == "DialHeadless" || selector.Sel.Name == "DialInteractive") {
				violations = append(violations, relPath+": startup entrypoint must not call remoteattach."+selector.Sel.Name+" directly")
			}
			return true
		})
		if relPath == runPath && !usesAttachRunPrompt {
			violations = append(violations, relPath+": run startup must attach through serverattach.AttachRunPrompt")
		}
		if relPath == sessionPath && !usesConfiguredDial {
			violations = append(violations, relPath+": pure-client startup must dial the configured remote")
		}
	}
	if len(violations) > 0 {
		t.Fatalf("startup server attach boundary violations:\n%s", strings.Join(violations, "\n"))
	}
}

func TestStartServeServerHasOneProductionCallSite(t *testing.T) {
	wantPath := filepath.Join("cli", "kent", "serve.go")
	repoRoot := findRepoRoot(t)
	references, violations := scanStartServeServerReferences(t, repoRoot)
	violations = append(violations, validateStartServeServerTopology(references, wantPath)...)
	if len(violations) > 0 {
		t.Fatalf("startup composition topology violations:\n%s", strings.Join(violations, "\n"))
	}
}

func validateStartServeServerTopology(references []startServeServerReference, wantPath string) []string {
	violations := make([]string, 0)
	if len(references) != 1 {
		violations = append(violations, fmt.Sprintf(
			"StartServeServer production reference count = %d, want exactly 1; references: %v",
			len(references),
			references,
		))
	} else {
		reference := references[0]
		if reference.RelPath != wantPath {
			violations = append(violations, fmt.Sprintf(
				"StartServeServer production reference = %s:%d:%d, want %s",
				reference.RelPath,
				reference.Position.Line,
				reference.Position.Column,
				wantPath,
			))
		}
		if !reference.DirectCall {
			violations = append(violations, reference.Position.String()+": StartServeServer must be the direct call target")
		}
	}
	return violations
}

func TestStartServeServerReferenceScanFindsIndirectAndBuildTaggedUses(t *testing.T) {
	root := t.TempDir()
	testsetup.WriteFile(t, filepath.Join(root, "go.mod"), "module core\n\ngo 1.26.4\n")
	testsetup.WriteFile(t, filepath.Join(root, "server/startup/start.go"), `package startup

func StartServeServer() {}

var samePackageAlias = StartServeServer
`)
	testsetup.WriteFile(t, filepath.Join(root, "cli/kent/serve.go"), `package kent

import "core/server/startup"

func serve() {
	startup.StartServeServer()
}
`)
	testsetup.WriteFile(t, filepath.Join(root, "cli/kent/alias.go"), `package kent

import "core/server/startup"

var indirect = startup.StartServeServer
`)
	testsetup.WriteFile(t, filepath.Join(root, "buildonly/alias.go"), `//go:build impossible

package buildonly

import "core/server/startup"

var buildOnly = startup.StartServeServer
`)

	references, violations := scanStartServeServerReferences(t, root)
	if len(violations) != 0 {
		t.Fatalf("fixture scan violations = %v", violations)
	}
	want := map[string]bool{
		filepath.Join("cli", "kent", "serve.go"):       true,
		filepath.Join("cli", "kent", "alias.go"):       false,
		filepath.Join("server", "startup", "start.go"): false,
		filepath.Join("buildonly", "alias.go"):         false,
	}
	if len(references) != 4 {
		t.Fatalf("fixture references = %+v, want direct plus three indirect references", references)
	}
	for _, reference := range references {
		direct, found := want[reference.RelPath]
		if !found || reference.DirectCall != direct {
			t.Fatalf("unexpected fixture reference: %+v", reference)
		}
	}
}

func TestStartServeServerReferenceScanIgnoresShadowedImportAlias(t *testing.T) {
	root := t.TempDir()
	testsetup.WriteFile(t, filepath.Join(root, "go.mod"), "module core\n\ngo 1.26.4\n")
	testsetup.WriteFile(t, filepath.Join(root, "server/startup/start.go"), `package startup

type Request struct{}

func StartServeServer() {}
`)
	testsetup.WriteFile(t, filepath.Join(root, "cli/kent/serve.go"), `package kent

import serverstartup "core/server/startup"

var _ serverstartup.Request

type localStartup struct{}

func (localStartup) StartServeServer() {}

func serve(serverstartup localStartup) {
	serverstartup.StartServeServer()
}
`)

	references, scanViolations := scanStartServeServerReferences(t, root)
	if len(scanViolations) != 0 {
		t.Fatalf("fixture scan violations = %v", scanViolations)
	}
	if len(references) != 0 {
		t.Fatalf("shadowed import alias references = %+v, want none", references)
	}
	topologyViolations := validateStartServeServerTopology(references, filepath.Join("cli", "kent", "serve.go"))
	if len(topologyViolations) != 1 {
		t.Fatalf("topology violation count = %d, want 1", len(topologyViolations))
	}
}

type startServeServerReference struct {
	RelPath    string
	Position   token.Position
	DirectCall bool
}

func scanStartServeServerReferences(t *testing.T, repoRoot string) ([]startServeServerReference, []string) {
	t.Helper()
	references := make([]startServeServerReference, 0)
	violations := make([]string, 0)
	recordReferences := func(source parsedGoSource) {
		directCalls := make(map[*ast.Ident]struct{})
		selectorNames := make(map[*ast.Ident]struct{})
		functionDeclarations := make(map[*ast.Ident]struct{})
		startupAliases := make(map[string]struct{})
		for _, spec := range source.File.Imports {
			importPath, err := strconv.Unquote(spec.Path.Value)
			if err != nil {
				violations = append(violations, source.RelPath+": decode import path: "+err.Error())
				continue
			}
			if importPath != "core/server/startup" {
				continue
			}
			alias := "startup"
			if spec.Name != nil {
				alias = spec.Name.Name
			}
			if alias == "." || alias == "_" {
				violations = append(violations, source.RelPath+": startup import must use a selector-capable package name")
				continue
			}
			startupAliases[alias] = struct{}{}
		}
		ast.Inspect(source.File, func(node ast.Node) bool {
			switch typed := node.(type) {
			case *ast.FuncDecl:
				functionDeclarations[typed.Name] = struct{}{}
			case *ast.SelectorExpr:
				selectorNames[typed.Sel] = struct{}{}
			case *ast.CallExpr:
				switch target := typed.Fun.(type) {
				case *ast.Ident:
					directCalls[target] = struct{}{}
				case *ast.SelectorExpr:
					directCalls[target.Sel] = struct{}{}
				}
			}
			return true
		})
		ast.Inspect(source.File, func(node ast.Node) bool {
			selector, ok := node.(*ast.SelectorExpr)
			if !ok || selector.Sel.Name != "StartServeServer" {
				return true
			}
			qualifier, ok := selector.X.(*ast.Ident)
			if !ok {
				return true
			}
			if qualifier.Obj != nil {
				return true
			}
			if _, importsStartup := startupAliases[qualifier.Name]; !importsStartup {
				return true
			}
			_, direct := directCalls[selector.Sel]
			references = append(references, startServeServerReference{
				RelPath: source.RelPath, Position: source.FileSet.Position(selector.Sel.Pos()), DirectCall: direct,
			})
			return true
		})
		if filepath.ToSlash(filepath.Dir(source.RelPath)) != "server/startup" {
			return
		}
		ast.Inspect(source.File, func(node ast.Node) bool {
			ident, ok := node.(*ast.Ident)
			if !ok || ident.Name != "StartServeServer" {
				return true
			}
			if _, declaration := functionDeclarations[ident]; declaration {
				return true
			}
			if _, selector := selectorNames[ident]; selector {
				return true
			}
			_, direct := directCalls[ident]
			references = append(references, startServeServerReference{
				RelPath: source.RelPath, Position: source.FileSet.Position(ident.Pos()), DirectCall: direct,
			})
			return true
		})
	}
	walkRepositoryGoSources(t, repoRoot, repositoryGoSourceScan{
		Operation:    "scan StartServeServer production references",
		Root:         ".",
		Recursive:    true,
		IncludeTests: false,
		Mode:         0,
		Selection:    allRepositoryGoSources{},
	}, recordReferences)
	sort.Slice(references, func(i, j int) bool {
		if references[i].RelPath != references[j].RelPath {
			return references[i].RelPath < references[j].RelPath
		}
		return references[i].Position.Offset < references[j].Position.Offset
	})
	return references, violations
}

type parsedGoSource struct {
	RelPath string
	File    *ast.File
	FileSet *token.FileSet
}

type repositoryGoSourceScan struct {
	Operation    string
	Root         string
	Recursive    bool
	IncludeTests bool
	Mode         parser.Mode
	Selection    repositoryGoSourceSelection
}

type repositoryGoSourceSelection interface {
	Include(path string) bool
}

type allRepositoryGoSources struct{}

func (allRepositoryGoSources) Include(string) bool {
	return true
}

type repositoryGoSourceBasePrefix struct {
	Prefix string
}

func (selection repositoryGoSourceBasePrefix) Include(path string) bool {
	return strings.HasPrefix(filepath.Base(path), selection.Prefix)
}

func walkRepositoryGoSources(t *testing.T, repoRoot string, scan repositoryGoSourceScan, visit func(parsedGoSource)) {
	t.Helper()
	root := filepath.Join(repoRoot, scan.Root)
	if err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path == filepath.Join(repoRoot, "tui-rs") {
				return filepath.SkipDir
			}
			if !scan.Recursive && path != root {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || (!scan.IncludeTests && strings.HasSuffix(path, "_test.go")) {
			return nil
		}
		if !scan.Selection.Include(path) {
			return nil
		}
		fileSet := token.NewFileSet()
		file, parseErr := parser.ParseFile(fileSet, path, nil, scan.Mode)
		if parseErr != nil {
			return parseErr
		}
		relPath, relErr := filepath.Rel(repoRoot, path)
		if relErr != nil {
			relPath = path
		}
		visit(parsedGoSource{RelPath: relPath, File: file, FileSet: fileSet})
		return nil
	}); err != nil {
		t.Fatalf("%s: %v", scan.Operation, err)
	}
}

func TestCLIAppSplitFilesDoNotImportServerPackages(t *testing.T) {
	repoRoot := findRepoRoot(t)
	files := []string{
		filepath.Join("cli", "app", "auth_oauth_presenter.go"),
		filepath.Join("cli", "app", "onboarding_render.go"),
		filepath.Join("cli", "app", "onboarding_workflow.go"),
		filepath.Join("cli", "app", "runtime_attachment.go"),
		filepath.Join("cli", "app", "ui_renderer_output_gate.go"),
		filepath.Join("cli", "app", "ui_process_render.go"),
		filepath.Join("cli", "app", "ui_runtime_client_control.go"),
		filepath.Join("cli", "app", "ui_status_overlay_format.go"),
	}
	violations := make([]string, 0)
	for _, relPath := range files {
		path := filepath.Join(repoRoot, relPath)
		fileSet := token.NewFileSet()
		file, parseErr := parser.ParseFile(fileSet, path, nil, parser.ImportsOnly)
		if parseErr != nil {
			t.Fatalf("parse %s imports: %v", relPath, parseErr)
		}
		for _, spec := range file.Imports {
			importPath := strings.Trim(spec.Path.Value, "\"")
			if strings.HasPrefix(importPath, "core/server/") && !isAllowedCLIAppRootServerImport(relPath, importPath) {
				violations = append(violations, relPath+": split app file must not import server package "+importPath)
			}
		}
	}
	if len(violations) > 0 {
		t.Fatalf("cli app split-file server import boundary violations:\n%s", strings.Join(violations, "\n"))
	}
}

func TestCLIOnboardingDoesNotOwnCapabilityFactDomains(t *testing.T) {
	repoRoot := findRepoRoot(t)
	violations := make([]string, 0)
	walkRepositoryGoSources(t, repoRoot, repositoryGoSourceScan{
		Operation:    "scan cli onboarding imports",
		Root:         filepath.Join("cli", "app"),
		Recursive:    false,
		IncludeTests: false,
		Mode:         parser.ImportsOnly,
		Selection:    repositoryGoSourceBasePrefix{Prefix: "onboarding"},
	}, func(source parsedGoSource) {
		for _, spec := range source.File.Imports {
			importPath := strings.Trim(spec.Path.Value, "\"")
			switch importPath {
			case "core/server/llm", "core/server/onboardingimports", "core/server/skillcatalog":
				violations = append(violations, source.RelPath+": onboarding capability facts must come from shared client/serverapi, not "+importPath)
			}
		}
	})

	internalRoot := filepath.Join(repoRoot, "cli", "app", "internal", "onboarding")
	if err := filepath.WalkDir(internalRoot, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || filepath.Ext(path) != ".go" {
			return nil
		}
		relPath, relErr := filepath.Rel(repoRoot, path)
		if relErr != nil {
			return relErr
		}
		violations = append(violations, relPath+": deleted onboarding adapter package must not be reintroduced")
		return nil
	}); err != nil && !os.IsNotExist(err) {
		t.Fatalf("scan deleted internal onboarding package: %v", err)
	}
	if len(violations) > 0 {
		t.Fatalf("cli onboarding capability-domain boundary violations:\n%s", strings.Join(violations, "\n"))
	}
}

func isAllowedCLIAppRootServerImport(relPath string, importPath string) bool {
	_, allowed := allowedCLIServerImports()[relPath][importPath]
	return allowed
}

func TestCLITUIFilesDoNotImportServerPackages(t *testing.T) {
	repoRoot := findRepoRoot(t)
	allowedServerImportsByFile := map[string]map[string]struct{}{}
	actualServerImportsByFile := make(map[string]map[string]struct{})
	violations := make([]string, 0)
	walkRepositoryGoSources(t, repoRoot, repositoryGoSourceScan{
		Operation:    "scan cli tui sources",
		Root:         filepath.Join("cli", "tui"),
		Recursive:    true,
		IncludeTests: false,
		Mode:         parser.ImportsOnly,
		Selection:    allRepositoryGoSources{},
	}, func(source parsedGoSource) {
		for _, spec := range source.File.Imports {
			importPath := strings.Trim(spec.Path.Value, "\"")
			if !strings.HasPrefix(importPath, "core/server/") {
				continue
			}
			if actualServerImportsByFile[source.RelPath] == nil {
				actualServerImportsByFile[source.RelPath] = make(map[string]struct{})
			}
			actualServerImportsByFile[source.RelPath][importPath] = struct{}{}
			if _, allowed := allowedServerImportsByFile[source.RelPath][importPath]; !allowed {
				violations = append(violations, source.RelPath+": TUI package must not add server import "+importPath)
			}
		}
	})
	for relPath, expectedImports := range allowedServerImportsByFile {
		actualImports := actualServerImportsByFile[relPath]
		for importPath := range expectedImports {
			if _, found := actualImports[importPath]; !found {
				violations = append(violations, relPath+": remove stale allowed server import "+importPath+" from architecture test")
			}
		}
	}
	if len(violations) > 0 {
		t.Fatalf("cli tui server import boundary violations:\n%s", strings.Join(violations, "\n"))
	}
}

type cliInternalBoundaryCase struct {
	Name                 string
	Packages             []string
	Label                string
	ForbidServer         bool
	ForbidAllCore        bool
	ServerViolationLabel string
}

func TestCLIAppInternalPackageBoundaries(t *testing.T) {
	cases := []cliInternalBoundaryCase{
		{Name: "Status", Packages: []string{"status"}, Label: "status package"},
		{Name: "RuntimeAttach", Packages: []string{"runtimeattach"}, Label: "runtime connection package", ForbidServer: true},
		{Name: "AuthUI", Packages: []string{"authui"}, Label: "auth UI package"},
		{Name: "ServerAttach", Packages: []string{"serverattach"}, Label: "server attach package", ForbidServer: true},
		{Name: "RemoteAttach", Packages: []string{"remoteattach"}, Label: "remote attach package", ForbidServer: true},
		{Name: "ProjectBinding", Packages: []string{"projectbinding"}, Label: "project binding package", ForbidServer: true},
		{Name: "WorktreeUI", Packages: []string{"worktreeui"}, Label: "worktree UI package", ForbidServer: true},
		{Name: "StartupConfig", Packages: []string{"startupconfig"}, Label: "startup config package"},
	}
	for _, tc := range cases {
		t.Run(tc.Name, func(t *testing.T) {
			assertCLIAppInternalPackageBoundary(t, tc)
		})
	}
}

func assertCLIAppInternalPackageBoundary(t *testing.T, tc cliInternalBoundaryCase) {
	t.Helper()
	repoRoot := findRepoRoot(t)
	violations := make([]string, 0)
	for _, packageName := range tc.Packages {
		walkRepositoryGoSources(t, repoRoot, repositoryGoSourceScan{
			Operation:    "scan cli app internal " + packageName + " sources",
			Root:         filepath.Join("cli", "app", "internal", packageName),
			Recursive:    true,
			IncludeTests: false,
			Mode:         parser.SkipObjectResolution,
			Selection:    allRepositoryGoSources{},
		}, func(source parsedGoSource) {
			for _, spec := range source.File.Imports {
				importPath := strings.Trim(spec.Path.Value, "\"")
				switch {
				case tc.ForbidAllCore && strings.HasPrefix(importPath, "core/"):
					violations = append(violations, source.RelPath+": "+tc.Label+" must not import core packages "+importPath)
				case tc.ForbidServer && strings.HasPrefix(importPath, "core/server/"):
					message := tc.ServerViolationLabel
					if message == "" {
						message = tc.Label + " must not import server package"
					}
					violations = append(violations, source.RelPath+": "+message+" "+importPath)
				case importPath == "github.com/charmbracelet/bubbletea":
					violations = append(violations, source.RelPath+": "+tc.Label+" must not import Bubble Tea")
				case importPath == "core/cli/app/commands":
					violations = append(violations, source.RelPath+": "+tc.Label+" must not import app commands")
				case importPath == "core/cli/app":
					violations = append(violations, source.RelPath+": "+tc.Label+" must not import app package")
				}
			}
			ast.Inspect(source.File, func(node ast.Node) bool {
				ident, ok := node.(*ast.Ident)
				if ok && ident.Name == "uiModel" {
					violations = append(violations, source.RelPath+": "+tc.Label+" must not reference uiModel")
				}
				return true
			})
		})
	}
	if len(violations) > 0 {
		t.Fatalf("cli app internal %s boundary violations:\n%s", tc.Name, strings.Join(violations, "\n"))
	}
}

func TestCLIDoesNotCallPersistenceStorageAPIsDirectly(t *testing.T) {
	repoRoot := findRepoRoot(t)
	forbiddenCalls := map[string]map[string]struct{}{
		"core/server/metadata": {
			"Open":                     {},
			"ResolveBinding":           {},
			"RegisterBinding":          {},
			"EnsureWorkspaceBinding":   {},
			"ResolveWorkspacePath":     {},
			"AttachWorkspaceToProject": {},
			"RebindWorkspace":          {},
		},
		"core/server/session": {
			"Open":         {},
			"OpenByID":     {},
			"Create":       {},
			"NewLazy":      {},
			"ListSessions": {},
		},
	}
	violations := make([]string, 0)
	walkRepositoryGoSources(t, repoRoot, repositoryGoSourceScan{
		Operation:    "scan cli sources",
		Root:         "cli",
		Recursive:    true,
		IncludeTests: false,
		Mode:         parser.SkipObjectResolution,
		Selection:    allRepositoryGoSources{},
	}, func(source parsedGoSource) {
		importAliases := make(map[string]string)
		for _, spec := range source.File.Imports {
			importPath := strings.Trim(spec.Path.Value, "\"")
			alias := ""
			if spec.Name != nil {
				alias = strings.TrimSpace(spec.Name.Name)
			} else {
				alias = filepath.Base(importPath)
			}
			importAliases[alias] = importPath
		}
		ast.Inspect(source.File, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			selector, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			ident, ok := selector.X.(*ast.Ident)
			if !ok {
				return true
			}
			importPath, ok := importAliases[ident.Name]
			if !ok {
				return true
			}
			forbiddenSelectors, ok := forbiddenCalls[importPath]
			if !ok {
				return true
			}
			if _, forbidden := forbiddenSelectors[selector.Sel.Name]; forbidden {
				violations = append(violations, source.RelPath+": frontend must not call "+importPath+"."+selector.Sel.Name)
			}
			return true
		})
	})
	if len(violations) > 0 {
		t.Fatalf("cli persistence boundary violations:\n%s", strings.Join(violations, "\n"))
	}
}

func loadRepoPackages(t *testing.T, repoRoot string) []goListPackage {
	t.Helper()
	cmd := exec.Command("go", "list", "-json", "./...")
	cmd.Dir = repoRoot
	cmd.Env = filteredGoListEnv()
	output, err := cmd.Output()
	if err != nil {
		t.Fatalf("go list packages: %v", err)
	}
	decoder := json.NewDecoder(strings.NewReader(string(output)))
	packages := make([]goListPackage, 0)
	for decoder.More() {
		var pkg goListPackage
		if err := decoder.Decode(&pkg); err != nil {
			t.Fatalf("decode go list package json: %v", err)
		}
		packages = append(packages, pkg)
	}
	return packages
}

func filteredGoListEnv() []string {
	env := os.Environ()
	filtered := make([]string, 0, len(env))
	for _, entry := range env {
		if strings.HasPrefix(entry, "ENV=") {
			continue
		}
		filtered = append(filtered, entry)
	}
	return filtered
}

func findRepoRoot(t *testing.T) string {
	return testsetup.RepositoryRoot(t)
}
