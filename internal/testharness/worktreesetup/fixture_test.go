package worktreesetup

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestValidateOptionsUsesExplicitOptionalEffects(t *testing.T) {
	defaults, err := validateOptions(Options{})
	if err != nil {
		t.Fatalf("validateOptions defaults: %v", err)
	}
	if defaults.markerRelativePath != nil {
		t.Fatalf("default marker = %q, want nil", *defaults.markerRelativePath)
	}
	if defaults.skill != nil {
		t.Fatalf("default skill = %+v, want nil", *defaults.skill)
	}
	if defaults.invocationCountRelativePath != filepath.Join(".kent", "setup-invocations") {
		t.Fatalf("default invocation count path = %q", defaults.invocationCountRelativePath)
	}

	marker := filepath.Join(".kent", "marker")
	count := filepath.Join(".kent", "invocations")
	configured, err := validateOptions(Options{
		MarkerRelativePath:          &marker,
		InvocationCountRelativePath: &count,
		Skill:                       &Skill{Name: "setup-skill", Description: "setup-created skill"},
	})
	if err != nil {
		t.Fatalf("validateOptions configured: %v", err)
	}
	if configured.markerRelativePath == nil || *configured.markerRelativePath != marker {
		t.Fatalf("marker = %v, want %q", configured.markerRelativePath, marker)
	}
	if configured.skill == nil || configured.skill.Name != "setup-skill" || configured.skill.Description != "setup-created skill" {
		t.Fatalf("skill = %+v", configured.skill)
	}
	if configured.invocationCountRelativePath != count {
		t.Fatalf("invocation count path = %q, want %q", configured.invocationCountRelativePath, count)
	}
}

func TestValidateOptionsRejectsInvalidPresentValues(t *testing.T) {
	empty := ""
	whitespace := " \t "
	ancestor := filepath.Join("..", "marker")
	for _, options := range []Options{
		{MarkerRelativePath: &empty},
		{InvocationCountRelativePath: &whitespace},
		{MarkerRelativePath: &ancestor},
		{Skill: &Skill{Name: empty, Description: "description"}},
		{Skill: &Skill{Name: "name", Description: whitespace}},
	} {
		if _, err := validateOptions(options); err == nil {
			t.Errorf("validateOptions(%+v) succeeded, want error", options)
		}
	}
}

func TestInstallInSourceWorkspaceReturnsRelativeExecutable(t *testing.T) {
	fixture := New(t, Options{})
	sourceWorkspace := t.TempDir()
	relativePath := fixture.InstallInSourceWorkspace(t, sourceWorkspace)
	if filepath.IsAbs(relativePath) {
		t.Fatalf("installed helper path = %q, want source-relative", relativePath)
	}
	info, err := os.Stat(filepath.Join(sourceWorkspace, relativePath))
	if err != nil {
		t.Fatalf("stat installed helper: %v", err)
	}
	if !info.Mode().IsRegular() {
		t.Fatalf("installed helper mode = %v, want regular file", info.Mode())
	}
	if runtime.GOOS != "windows" && info.Mode()&0o111 == 0 {
		t.Fatalf("installed helper mode = %v, want executable file", info.Mode())
	}
}
