package core_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"reflect"
	"strconv"
	"testing"

	"core/shared/protocol"
	"core/shared/serverapi"
)

func TestPromptFollowUpContractsCarryIdentityAndTerminalOnlyEvents(t *testing.T) {
	assertStructFields(t, reflect.TypeOf(serverapi.PromptFollowUpWatchRequest{}), []string{"PromptID", "SessionID", "StepID"})
	assertStructFields(t, reflect.TypeOf(serverapi.PromptFollowUpEvent{}), []string{"Kind"})
	assertStructFields(t, reflect.TypeOf(protocol.PromptFollowUpEventParams{}), []string{"Event"})
	assertStructFields(t, reflect.TypeOf(protocol.PromptFollowUpEvent{}), []string{"Kind"})
}
func TestObsoletePromptMutationSymbolsStayDeleted(t *testing.T) {
	forbiddenIdentifiers := map[string]struct{}{
		"Ask" + "AnswerRequest": {}, "Approval" + "AnswerRequest": {}, "Answer" + "Ask": {},
		"Answer" + "Approval": {}, "AnswerWorkflowTask" + "Question": {}, "WorkflowTaskQuestion" + "AnswerRequest": {},
		"WorkflowTaskQuestionApproval" + "Answer": {}, "MethodAsk" + "Answer": {}, "MethodApproval" + "Answer": {},
		"MethodWorkflowTaskQuestion" + "Answer": {}, "PromptResponse" + "Acceptance": {}, "AcceptPrompt" + "Resolution": {},
		"AcceptPromptResolution" + "ForScope": {}, "SubmitPrompt" + "Resolution": {}, "SubmitPromptResolution" + "ForScope": {},
		"ResolvePendingWorkflow" + "Prompt": {}, "WorkflowPrompt" + "Resolution": {},
		"ErrWorkflowPrompt" + "Ambiguous": {}, "ErrWorkflowTaskQuestionSelector" + "Ambiguous": {},
	}
	forbiddenWireValues := map[string]struct{}{"ask." + "answer": {}, "approval." + "answer": {}, "workflow.task.question." + "answer": {}}
	walkRepositoryGoSources(t, findRepoRoot(t), repositoryGoSourceScan{
		Operation: "scan prompt answer hard cutover", Root: ".", Recursive: true,
		IncludeTests: false, Mode: parser.SkipObjectResolution, Selection: allRepositoryGoSources{},
	}, func(source parsedGoSource) {
		ast.Inspect(source.File, func(node ast.Node) bool {
			switch typed := node.(type) {
			case *ast.Ident:
				if _, forbidden := forbiddenIdentifiers[typed.Name]; forbidden {
					t.Errorf("%s contains obsolete identifier %s", source.RelPath, typed.Name)
				}
			case *ast.BasicLit:
				if value, err := strconv.Unquote(typed.Value); typed.Kind == token.STRING && err == nil {
					if _, forbidden := forbiddenWireValues[value]; forbidden {
						t.Errorf("%s contains obsolete wire method %s", source.RelPath, value)
					}
				}
			case *ast.SelectorExpr:
				if typed.Sel.Name == "SubscribeFollowUp" && source.RelPath != "cli/kent/question_command.go" &&
					source.RelPath != "shared/client/remote_stream.go" &&
					source.RelPath != "server/promptcontrol/service.go" &&
					source.RelPath != "server/transport/gateway_stream_handlers.go" {
					t.Errorf("%s contains unauthorized SubscribeFollowUp consumer", source.RelPath)
				}
			}
			return true
		})
	})
}
func assertStructFields(t *testing.T, contract reflect.Type, want []string) {
	t.Helper()
	if contract.NumField() != len(want) {
		t.Fatalf("%s field count = %d, want %d", contract.Name(), contract.NumField(), len(want))
	}
	for _, field := range want {
		if _, exists := contract.FieldByName(field); !exists {
			t.Fatalf("%s missing field %s", contract.Name(), field)
		}
	}
}
