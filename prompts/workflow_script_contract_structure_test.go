package prompts

import (
	"bytes"
	"encoding/json"
	"io/fs"
	"strings"
	"testing"

	"core/server/workflowscript"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/text"
)

type workflowScriptContractFindingKind string

const (
	findingInvalidWorkflowScriptIdentity    workflowScriptContractFindingKind = "invalid_workflow_script_identity"
	findingIncompleteWorkflowScriptIdentity workflowScriptContractFindingKind = "incomplete_workflow_script_identity"
	findingMissingWorkflowScriptIdentity    workflowScriptContractFindingKind = "missing_workflow_script_identity"
)

type workflowScriptContractFinding struct {
	kind workflowScriptContractFindingKind
}

func TestEmbeddedWorkflowScriptExamplesUseCurrentNodeIdentity(t *testing.T) {
	source, err := fs.ReadFile(GeneratedSkillsFS, "skills/kent-workflows/SKILL.md")
	if err != nil {
		t.Fatalf("read embedded workflow skill: %v", err)
	}
	findings := analyzeWorkflowScriptExamples(source)
	if len(findings) > 0 {
		t.Fatalf("embedded workflow Script example findings = %+v", findings)
	}
}

func TestEmbeddedWorkflowScriptExampleGuardRejectsDuplicateAuthority(t *testing.T) {
	t.Run("unknown execution identity", func(t *testing.T) {
		findings := analyzeWorkflowScriptExamples([]byte("```json\n" + `{
  "_kent": {
    "task_id": "task-1",
    "node_id": "node-1",
    "execution_id": "scope-1"
  }
}` + "\n```\n"))
		assertWorkflowScriptFinding(t, findings, findingInvalidWorkflowScriptIdentity)
	})

	t.Run("missing Current Node identity", func(t *testing.T) {
		findings := analyzeWorkflowScriptExamples([]byte("```json\n" + `{
  "_kent": {
    "task_id": "task-1"
  }
}` + "\n```\n"))
		assertWorkflowScriptFinding(t, findings, findingIncompleteWorkflowScriptIdentity)
	})
}

func analyzeWorkflowScriptExamples(source []byte) []workflowScriptContractFinding {
	document := goldmark.New().Parser().Parse(text.NewReader(source))
	var findings []workflowScriptContractFinding
	identityExamples := 0
	_ = ast.Walk(document, func(node ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		block, ok := node.(*ast.FencedCodeBlock)
		if !ok || strings.TrimSpace(string(block.Language(source))) != "json" {
			return ast.WalkContinue, nil
		}
		var body bytes.Buffer
		for index := 0; index < block.Lines().Len(); index++ {
			segment := block.Lines().At(index)
			body.Write(segment.Value(source))
		}
		var object map[string]json.RawMessage
		decoder := json.NewDecoder(bytes.NewReader(body.Bytes()))
		if err := decoder.Decode(&object); err != nil {
			return ast.WalkContinue, nil
		}
		rawIdentity, ok := object["_kent"]
		if !ok {
			return ast.WalkContinue, nil
		}
		identityExamples++
		var identity workflowscript.CurrentNodeIdentity
		identityDecoder := json.NewDecoder(bytes.NewReader(rawIdentity))
		identityDecoder.DisallowUnknownFields()
		if err := identityDecoder.Decode(&identity); err != nil || identityDecoder.Decode(&struct{}{}) == nil {
			findings = append(findings, workflowScriptContractFinding{kind: findingInvalidWorkflowScriptIdentity})
			return ast.WalkContinue, nil
		}
		if err := identity.Validate(); err != nil {
			findings = append(findings, workflowScriptContractFinding{kind: findingIncompleteWorkflowScriptIdentity})
		}
		return ast.WalkContinue, nil
	})
	if identityExamples == 0 {
		findings = append(findings, workflowScriptContractFinding{kind: findingMissingWorkflowScriptIdentity})
	}
	return findings
}

func assertWorkflowScriptFinding(
	t *testing.T,
	findings []workflowScriptContractFinding,
	want workflowScriptContractFindingKind,
) {
	t.Helper()
	for _, finding := range findings {
		if finding.kind == want {
			return
		}
	}
	t.Fatalf("findings = %+v, want category %s", findings, want)
}
