package core_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
	"mvdan.cc/sh/v3/syntax"
)

type releaseWorkflow struct {
	Jobs map[string]releaseWorkflowJob `yaml:"jobs"`
}

type releaseWorkflowJob struct {
	Needs releaseWorkflowNeeds  `yaml:"needs"`
	Steps []releaseWorkflowStep `yaml:"steps"`
}

type releaseWorkflowNeeds []string

type releaseWorkflowStep struct {
	ID   string                  `yaml:"id"`
	Run  string                  `yaml:"run"`
	With releaseWorkflowStepWith `yaml:"with"`
}

type releaseWorkflowStepWith struct {
	Name  *string `yaml:"name"`
	Path  *string `yaml:"path"`
	Files *string `yaml:"files"`
	Draft *bool   `yaml:"draft"`
}

type desktopArtifactDownload struct {
	name string
	path string
}

func TestReleaseWorkflowPublishesAllDesktopPlatformsFailClosed(t *testing.T) {
	repoRoot := findRepoRoot(t)
	workflowPath := filepath.Join(repoRoot, ".github", "workflows", "release.yml")
	content, err := os.ReadFile(workflowPath)
	if err != nil {
		t.Fatalf("read %s: %v", workflowPath, err)
	}

	var workflow releaseWorkflow
	if err := yaml.Unmarshal(content, &workflow); err != nil {
		t.Fatalf("parse %s: %v", workflowPath, err)
	}

	publishDesktop, ok := workflow.Jobs["publish_desktop"]
	if !ok {
		t.Fatal("release workflow is missing publish_desktop")
	}
	if !containsWorkflowNeed(publishDesktop.Needs, "build_desktop") {
		t.Error("publish_desktop must require build_desktop")
	}

	expectedDownloads := []desktopArtifactDownload{
		{name: "desktop-assets-macos-${{ needs.prepare_release.outputs.tag }}", path: "dist/desktop"},
		{name: "desktop-assets-linux-${{ needs.prepare_release.outputs.tag }}", path: "dist/desktop"},
		{name: "desktop-assets-windows-${{ needs.prepare_release.outputs.tag }}", path: "dist/desktop"},
	}
	allDownloadIndexes := workflowStepIndexes(publishDesktop.Steps, func(step releaseWorkflowStep) bool {
		return step.With.Name != nil && yamlStringEquals(step.With.Path, "dist/desktop")
	})
	if len(allDownloadIndexes) != len(expectedDownloads) {
		t.Errorf("publish_desktop has %d artifact tuples targeting dist/desktop, want exactly %d", len(allDownloadIndexes), len(expectedDownloads))
	}
	downloadIndexes := make([]int, 0, len(expectedDownloads))
	for _, expected := range expectedDownloads {
		indexes := workflowStepIndexes(publishDesktop.Steps, func(step releaseWorkflowStep) bool {
			return yamlStringEquals(step.With.Name, expected.name) &&
				yamlStringEquals(step.With.Path, expected.path)
		})
		if len(indexes) != 1 {
			t.Errorf("publish_desktop artifact tuple name=%q path=%q appears %d times, want exactly once", expected.name, expected.path, len(indexes))
			continue
		}
		downloadIndexes = append(downloadIndexes, indexes[0])
	}

	assemblerIndexes := workflowStepIndexes(publishDesktop.Steps, func(step releaseWorkflowStep) bool {
		return step.ID == "assemble_desktop"
	})
	if len(assemblerIndexes) != 1 {
		t.Errorf("publish_desktop has %d steps with id assemble_desktop, want exactly one", len(assemblerIndexes))
	}
	var assemblerIndex int
	if len(assemblerIndexes) == 1 {
		assemblerIndex = assemblerIndexes[0]
		if err := validateDesktopAssemblerRun(publishDesktop.Steps[assemblerIndex].Run); err != nil {
			t.Errorf("publish_desktop step assemble_desktop: %v", err)
		}
	}

	publicationIndexes := workflowStepIndexes(publishDesktop.Steps, func(step releaseWorkflowStep) bool {
		return yamlStringEquals(step.With.Files, "dist/desktop/*") &&
			step.With.Draft != nil &&
			!*step.With.Draft
	})
	if len(publicationIndexes) != 1 {
		t.Errorf("publish_desktop has %d public release steps with files=dist/desktop/* and draft=false, want exactly one", len(publicationIndexes))
	}

	if len(downloadIndexes) == len(expectedDownloads) && len(assemblerIndexes) == 1 && len(publicationIndexes) == 1 {
		for _, downloadIndex := range downloadIndexes {
			if downloadIndex >= assemblerIndex {
				t.Errorf("desktop artifact download at step %d must occur before assemble_desktop at step %d", downloadIndex, assemblerIndex)
			}
		}
		if assemblerIndex >= publicationIndexes[0] {
			t.Errorf("assemble_desktop at step %d must occur before public release at step %d", assemblerIndex, publicationIndexes[0])
		}
	}
}

func TestDesktopAssemblerRunSemanticContract(t *testing.T) {
	testCases := []struct {
		name    string
		run     string
		wantErr bool
	}{
		{
			name: "accepts semantic command with reordered options and different quoting",
			run:  `bash scripts/desktop-release.sh assemble --base-url "https://github.com/${GITHUB_REPOSITORY}/releases/download/${TAG}" --dist-dir 'dist/desktop' --version ${KENT_VERSION}`,
		},
		{
			name:    "rejects another version source",
			run:     `bash scripts/desktop-release.sh assemble --version "$VERSION" --dist-dir dist/desktop --base-url "https://github.com/${GITHUB_REPOSITORY}/releases/download/${TAG}"`,
			wantErr: true,
		},
		{
			name:    "rejects another release URL",
			run:     `bash scripts/desktop-release.sh assemble --version "$KENT_VERSION" --dist-dir dist/desktop --base-url "https://wrong.example/${GITHUB_REPOSITORY}/${TAG}"`,
			wantErr: true,
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			err := validateDesktopAssemblerRun(testCase.run)
			if testCase.wantErr && err == nil {
				t.Fatal("assembler contract unexpectedly accepted invalid command")
			}
			if !testCase.wantErr && err != nil {
				t.Fatalf("assembler contract rejected valid command: %v", err)
			}
		})
	}
}

func (needs *releaseWorkflowNeeds) UnmarshalYAML(node *yaml.Node) error {
	switch node.Kind {
	case yaml.ScalarNode:
		*needs = []string{node.Value}
		return nil
	case yaml.SequenceNode:
		return node.Decode((*[]string)(needs))
	default:
		return &yaml.TypeError{Errors: []string{"workflow needs must be a string or sequence"}}
	}
}

func containsWorkflowNeed(needs releaseWorkflowNeeds, expected string) bool {
	for _, need := range needs {
		if need == expected {
			return true
		}
	}
	return false
}

func yamlStringEquals(value *string, expected string) bool {
	return value != nil && *value == expected
}

func validateDesktopAssemblerRun(run string) error {
	file, err := syntax.NewParser(syntax.Variant(syntax.LangBash)).Parse(strings.NewReader(run), "assemble_desktop")
	if err != nil {
		return fmt.Errorf("parse run command: %w", err)
	}
	if len(file.Stmts) != 1 {
		return fmt.Errorf("run command has %d statements, want exactly one", len(file.Stmts))
	}
	statement := file.Stmts[0]
	if statement.Background || statement.Negated || statement.Coprocess || len(statement.Redirs) != 0 {
		return fmt.Errorf("run command must be one foreground command without negation or redirection")
	}
	call, ok := statement.Cmd.(*syntax.CallExpr)
	if !ok || len(call.Assigns) != 0 {
		return fmt.Errorf("run command must be a direct command invocation")
	}
	if len(call.Args) < 3 ||
		call.Args[0].Lit() != "bash" ||
		call.Args[1].Lit() != "scripts/desktop-release.sh" ||
		call.Args[2].Lit() != "assemble" {
		return fmt.Errorf("run command must invoke bash scripts/desktop-release.sh assemble")
	}
	if (len(call.Args)-3)%2 != 0 {
		return fmt.Errorf("assembler arguments must be flag/value pairs")
	}
	options := make(map[string]*syntax.Word)
	for index := 3; index < len(call.Args); index += 2 {
		flag := call.Args[index].Lit()
		switch flag {
		case "--version", "--dist-dir", "--base-url":
		default:
			return fmt.Errorf("assembler uses unsupported option %q", flag)
		}
		if _, duplicated := options[flag]; duplicated {
			return fmt.Errorf("assembler repeats option %q", flag)
		}
		options[flag] = call.Args[index+1]
	}
	for _, required := range []string{"--version", "--dist-dir", "--base-url"} {
		if _, found := options[required]; !found {
			return fmt.Errorf("assembler is missing required option %q", required)
		}
	}
	if !shellWordIsLiteral(options["--dist-dir"], "dist/desktop") {
		return fmt.Errorf("assembler --dist-dir must be dist/desktop")
	}
	if !shellWordIsParameter(options["--version"], "KENT_VERSION") {
		return fmt.Errorf("assembler --version must use KENT_VERSION")
	}
	if !shellWordIsReleaseURL(options["--base-url"]) {
		return fmt.Errorf("assembler --base-url must use the canonical GitHub repository/tag release URL")
	}
	return nil
}

func shellWordIsLiteral(word *syntax.Word, expected string) bool {
	parts, ok := flattenShellWordParts(word)
	return ok && len(parts) == 1 && shellWordPartIsLiteral(parts[0], expected)
}

func shellWordIsParameter(word *syntax.Word, name string) bool {
	parts, ok := flattenShellWordParts(word)
	return ok && len(parts) == 1 && shellWordPartIsParameter(parts[0], name)
}

func shellWordIsReleaseURL(word *syntax.Word) bool {
	parts, ok := flattenShellWordParts(word)
	if !ok || len(parts) != 4 {
		return false
	}
	return shellWordPartIsLiteral(parts[0], "https://github.com/") &&
		shellWordPartIsParameter(parts[1], "GITHUB_REPOSITORY") &&
		shellWordPartIsLiteral(parts[2], "/releases/download/") &&
		shellWordPartIsParameter(parts[3], "TAG")
}

func flattenShellWordParts(word *syntax.Word) ([]syntax.WordPart, bool) {
	parts := make([]syntax.WordPart, 0, len(word.Parts))
	for _, part := range word.Parts {
		quoted, isDoubleQuoted := part.(*syntax.DblQuoted)
		if !isDoubleQuoted {
			parts = append(parts, part)
			continue
		}
		if quoted.Dollar {
			return nil, false
		}
		parts = append(parts, quoted.Parts...)
	}
	return parts, true
}

func shellWordPartIsLiteral(part syntax.WordPart, expected string) bool {
	switch value := part.(type) {
	case *syntax.Lit:
		return value.Value == expected
	case *syntax.SglQuoted:
		return !value.Dollar && value.Value == expected
	default:
		return false
	}
}

func shellWordPartIsParameter(part syntax.WordPart, name string) bool {
	parameter, ok := part.(*syntax.ParamExp)
	return ok &&
		parameter.Param != nil &&
		parameter.Param.Value == name &&
		parameter.Flags == nil &&
		!parameter.Excl &&
		!parameter.Length &&
		!parameter.Width &&
		!parameter.IsSet &&
		parameter.NestedParam == nil &&
		parameter.Index == nil &&
		len(parameter.Modifiers) == 0 &&
		parameter.Slice == nil &&
		parameter.Repl == nil &&
		parameter.Names == 0 &&
		parameter.Exp == nil
}

func workflowStepIndexes(steps []releaseWorkflowStep, matches func(releaseWorkflowStep) bool) []int {
	var indexes []int
	for index, step := range steps {
		if matches(step) {
			indexes = append(indexes, index)
		}
	}
	return indexes
}
