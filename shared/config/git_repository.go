package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

type GitRepositoryInspection interface {
	isGitRepositoryInspection()
}

type GitRepositoryPresent struct{}

type GitNotRepository struct{}

type GitRepositoryInspectionFailed struct {
	Cause error
}

func (GitRepositoryPresent) isGitRepositoryInspection()          {}
func (GitNotRepository) isGitRepositoryInspection()              {}
func (GitRepositoryInspectionFailed) isGitRepositoryInspection() {}

func InspectGitRepository(workdir string) GitRepositoryInspection {
	info, err := os.Stat(workdir)
	if err != nil {
		return GitRepositoryInspectionFailed{Cause: fmt.Errorf("inspect git workdir: %w", err)}
	}
	if !info.IsDir() {
		return GitNotRepository{}
	}
	current := filepath.Clean(workdir)
	if resolved, err := filepath.EvalSymlinks(current); err == nil {
		current = filepath.Clean(resolved)
	} else {
		return GitRepositoryInspectionFailed{Cause: fmt.Errorf("resolve git workdir: %w", err)}
	}
	for {
		gitMetadataPath := filepath.Join(current, ".git")
		if _, err := os.Lstat(gitMetadataPath); err == nil {
			return GitRepositoryPresent{}
		} else if !errors.Is(err, os.ErrNotExist) {
			return GitRepositoryInspectionFailed{Cause: fmt.Errorf("inspect git metadata: %w", err)}
		}
		parent := filepath.Dir(current)
		if parent == current {
			return GitNotRepository{}
		}
		current = parent
	}
}
