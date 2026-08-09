package core_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
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
		"Answer" + "Approval": {}, "AnswerWorkflowTask" + "Question": {},
		"WorkflowTaskQuestion" + "AnswerRequest": {}, "WorkflowTaskQuestionApproval" + "Answer": {},
		"MethodAsk" + "Answer": {}, "MethodApproval" + "Answer": {},
		"MethodWorkflowTaskQuestion" + "Answer": {}, "PromptResponse" + "Acceptance": {},
		"AcceptPrompt" + "Resolution": {}, "AcceptPromptResolution" + "ForScope": {},
		"SubmitPrompt" + "Resolution": {}, "SubmitPromptResolution" + "ForScope": {},
		"ResolvePendingWorkflow" + "Prompt": {}, "WorkflowPrompt" + "Resolution": {},
		"ErrWorkflowPrompt" + "Ambiguous": {}, "ErrWorkflowTaskQuestionSelector" + "Ambiguous": {},
	}
	forbiddenWireValues := map[string]struct{}{
		"ask." + "answer": {}, "approval." + "answer": {}, "workflow.task.question." + "answer": {},
	}
	var findings []string
	repoRoot := findRepoRoot(t)
	for _, relPath := range repositoryGoSourcePaths(t, repoRoot) {
		if strings.HasSuffix(relPath, "_test.go") {
			continue
		}
		fileSet := token.NewFileSet()
		file, err := parser.ParseFile(fileSet, filepath.Join(repoRoot, relPath), nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("parse %s: %v", relPath, err)
		}
		ast.Inspect(file, func(node ast.Node) bool {
			switch typed := node.(type) {
			case *ast.Ident:
				if _, forbidden := forbiddenIdentifiers[typed.Name]; forbidden {
					findings = append(findings, relPath+":"+strconv.Itoa(fileSet.Position(typed.Pos()).Line)+": "+typed.Name)
				}
			case *ast.BasicLit:
				if typed.Kind != token.STRING {
					break
				}
				value, err := strconv.Unquote(typed.Value)
				if err == nil {
					if _, forbidden := forbiddenWireValues[value]; forbidden {
						findings = append(findings, relPath+":"+strconv.Itoa(fileSet.Position(typed.Pos()).Line)+": "+value)
					}
				}
			case *ast.SelectorExpr:
				if typed.Sel.Name == "SubscribeFollowUp" && relPath != "cli/kent/question_command.go" &&
					relPath != "shared/client/remote_stream.go" &&
					relPath != "server/promptcontrol/service.go" &&
					relPath != "server/transport/gateway_stream_handlers.go" {
					findings = append(findings, relPath+":"+strconv.Itoa(fileSet.Position(typed.Pos()).Line)+": unauthorized SubscribeFollowUp consumer")
				}
			}
			return true
		})
	}
	sort.Strings(findings)
	if len(findings) != 0 {
		t.Fatalf("obsolete or unauthorized prompt-answer symbols:\n%s", strings.Join(findings, "\n"))
	}
}

func assertStructFields(t *testing.T, contract reflect.Type, want []string) {
	t.Helper()
	got := make([]string, 0, contract.NumField())
	for index := 0; index < contract.NumField(); index++ {
		got = append(got, contract.Field(index).Name)
	}
	sort.Strings(got)
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%s fields = %v, want %v", contract.Name(), got, want)
	}
}
