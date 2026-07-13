package core_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
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
	Uses string                  `yaml:"uses"`
	With releaseWorkflowStepWith `yaml:"with"`
}

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
	desktopArtifactDownloadAction = "actions/download-artifact@v8.0.1"
	desktopPublicationAction      = "softprops/action-gh-release@v3.0.1"
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
		return step.Uses == desktopArtifactDownloadAction
	})
	if len(allDownloadIndexes) != len(expectedDownloads) {
		t.Errorf("publish_desktop has %d actions/download-artifact steps, want exactly %d", len(allDownloadIndexes), len(expectedDownloads))
	}
	downloadIndexes := make([]int, 0, len(expectedDownloads))
	for _, expected := range expectedDownloads {
		indexes := workflowStepIndexes(publishDesktop.Steps, func(step releaseWorkflowStep) bool {
			return step.Uses == desktopArtifactDownloadAction &&
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
		const expectedAssemblerRun = `bash scripts/desktop-release.sh assemble \
  --version "$KENT_VERSION" \
  --dist-dir dist/desktop \
  --base-url "https://github.com/${{ github.repository }}/releases/download/${TAG}"`
		if strings.TrimSpace(publishDesktop.Steps[assemblerIndex].Run) != expectedAssemblerRun {
			t.Errorf("publish_desktop step assemble_desktop run command does not match the canonical desktop assembler invocation")
		}
	}

	publicationIndexes := workflowStepIndexes(publishDesktop.Steps, func(step releaseWorkflowStep) bool {
		return step.Uses == desktopPublicationAction &&
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

func workflowStepIndexes(steps []releaseWorkflowStep, matches func(releaseWorkflowStep) bool) []int {
	var indexes []int
	for index, step := range steps {
		if matches(step) {
			indexes = append(indexes, index)
		}
	}
	return indexes
}
