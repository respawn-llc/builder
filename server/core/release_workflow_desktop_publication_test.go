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
	ID   string                          `yaml:"id"`
	Run  string                          `yaml:"run"`
	Uses *releaseWorkflowActionReference `yaml:"uses"`
	With releaseWorkflowStepWith         `yaml:"with"`
}

type releaseWorkflowActionReference struct {
	Repository releaseWorkflowActionRepository
}

type releaseWorkflowActionRepository string

type releaseWorkflowStepWith struct {
	Name  string `yaml:"name"`
	Path  string `yaml:"path"`
	Files string `yaml:"files"`
	Draft *bool  `yaml:"draft"`
}

type desktopArtifactDownload struct {
	name string
	path string
}

const (
	desktopArtifactDownloadAction releaseWorkflowActionRepository = "actions/download-artifact"
	desktopPublicationAction      releaseWorkflowActionRepository = "softprops/action-gh-release"
)

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
		return workflowStepUsesAction(step, desktopArtifactDownloadAction)
	})
	if len(allDownloadIndexes) != len(expectedDownloads) {
		t.Errorf("publish_desktop has %d actions/download-artifact steps, want exactly %d", len(allDownloadIndexes), len(expectedDownloads))
	}
	downloadIndexes := make([]int, 0, len(expectedDownloads))
	for _, expected := range expectedDownloads {
		indexes := workflowStepIndexes(publishDesktop.Steps, func(step releaseWorkflowStep) bool {
			return workflowStepUsesAction(step, desktopArtifactDownloadAction) &&
				step.With.Name == expected.name &&
				step.With.Path == expected.path
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
		return workflowStepUsesAction(step, desktopPublicationAction) &&
			step.With.Files == "dist/desktop/*" &&
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

func (action *releaseWorkflowActionReference) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind != yaml.ScalarNode {
		return fmt.Errorf("workflow action reference must be a scalar")
	}
	repository, revision, found := strings.Cut(node.Value, "@")
	if !found || repository == "" || revision == "" {
		return fmt.Errorf("workflow action reference %q must use repository@revision syntax", node.Value)
	}
	action.Repository = releaseWorkflowActionRepository(repository)
	return nil
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

func workflowStepUsesAction(step releaseWorkflowStep, repository releaseWorkflowActionRepository) bool {
	return step.Uses != nil && step.Uses.Repository == repository
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
	if options["--dist-dir"].Lit() != "dist/desktop" {
		return fmt.Errorf("assembler --dist-dir must be dist/desktop")
	}
	return nil
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
