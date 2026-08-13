package protogen

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/gofrs/flock"
)

const fingerprintVersion = "kent-protobuf-generation-v1"

type Target struct {
	Name       string
	Template   string
	OutputPath string
	Inputs     []string
}

var Targets = map[string]Target{
	"go": {
		Name:       "go",
		Template:   "buf.gen.go.yaml",
		OutputPath: "shared/protoapi/gen",
		Inputs: []string{
			"tools/protobuf/protoc-gen-kent-go-registry",
		},
	},
	"ts": {
		Name:       "ts",
		Template:   "buf.gen.ts.yaml",
		OutputPath: "apps/desktop/packages/server-api-contract/src/gen",
		Inputs: []string{
			"tools/protobuf/package.json",
			"tools/protobuf/pnpm-lock.yaml",
			"tools/protobuf/protoc-gen-kent-ts-registry",
		},
	},
}

type manifest struct {
	InputFingerprint  string `json:"input_fingerprint"`
	OutputFingerprint string `json:"output_fingerprint"`
}

type Manager struct {
	RepositoryRoot string
	Generate       func(target Target, destination string) error
	BeforeReplace  func(target Target, destination string) error
}

func NewManager(repositoryRoot string) *Manager {
	manager := &Manager{RepositoryRoot: repositoryRoot}
	manager.Generate = manager.generate
	return manager
}

func (m *Manager) Ensure(targets []Target) error {
	return m.withLock(func() error {
		for _, target := range targets {
			current, err := m.isCurrent(target)
			if err != nil {
				return err
			}
			if current {
				continue
			}
			if err := m.replace(target); err != nil {
				return err
			}
		}
		return nil
	})
}

func (m *Manager) GenerateTargets(targets []Target) error {
	return m.withLock(func() error {
		for _, target := range targets {
			if err := m.replace(target); err != nil {
				return err
			}
		}
		return nil
	})
}

func (m *Manager) Verify(targets []Target) error {
	return m.withLock(func() error {
		for _, target := range targets {
			if err := m.replace(target); err != nil {
				return err
			}
			expected, err := fingerprintTree(filepath.Join(m.RepositoryRoot, target.OutputPath))
			if err != nil {
				return err
			}
			staging, err := m.generateStaging(target)
			if err != nil {
				return err
			}
			actual, fingerprintErr := fingerprintTree(filepath.Join(staging, target.OutputPath))
			cleanupErr := os.RemoveAll(staging)
			if fingerprintErr != nil {
				return fingerprintErr
			}
			if cleanupErr != nil {
				return cleanupErr
			}
			if actual != expected {
				return fmt.Errorf("%s Protobuf generation is not deterministic", target.Name)
			}
		}
		return nil
	})
}

func (m *Manager) isCurrent(target Target) (bool, error) {
	inputFingerprint, err := m.inputFingerprint(target)
	if err != nil {
		return false, err
	}
	content, err := os.ReadFile(m.manifestPath(target))
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var stored manifest
	if err := json.Unmarshal(content, &stored); err != nil {
		return false, nil
	}
	if stored.InputFingerprint != inputFingerprint {
		return false, nil
	}
	outputFingerprint, err := fingerprintTree(filepath.Join(m.RepositoryRoot, target.OutputPath))
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return stored.OutputFingerprint == outputFingerprint, nil
}

func (m *Manager) replace(target Target) error {
	staging, err := m.generateStaging(target)
	if err != nil {
		return err
	}
	defer os.RemoveAll(staging)

	stagedOutput := filepath.Join(staging, target.OutputPath)
	outputFingerprint, err := fingerprintTree(stagedOutput)
	if err != nil {
		return fmt.Errorf("%s generation did not produce %s: %w", target.Name, target.OutputPath, err)
	}
	inputFingerprint, err := m.inputFingerprint(target)
	if err != nil {
		return err
	}
	destination := filepath.Join(m.RepositoryRoot, target.OutputPath)
	if m.BeforeReplace != nil {
		if err := m.BeforeReplace(target, destination); err != nil {
			return err
		}
	}
	if err := replaceDirectory(stagedOutput, destination); err != nil {
		return err
	}
	return m.writeManifest(target, manifest{
		InputFingerprint:  inputFingerprint,
		OutputFingerprint: outputFingerprint,
	})
}

func (m *Manager) generateStaging(target Target) (string, error) {
	stagingRoot := filepath.Join(m.RepositoryRoot, ".generated", "protobuf")
	if err := os.MkdirAll(stagingRoot, 0o755); err != nil {
		return "", err
	}
	staging, err := os.MkdirTemp(stagingRoot, "staging-")
	if err != nil {
		return "", err
	}
	if err := m.Generate(target, staging); err != nil {
		_ = os.RemoveAll(staging)
		return "", fmt.Errorf("generate %s Protobuf contract: %w", target.Name, err)
	}
	return staging, nil
}

func (m *Manager) generate(target Target, destination string) error {
	if target.Name == "ts" {
		if err := m.ensureTypeScriptGenerator(); err != nil {
			return err
		}
	}
	goCommand := os.Getenv("KENT_PROTOBUF_GO_COMMAND")
	if goCommand == "" {
		goCommand = "go"
	}
	command := exec.Command(
		goCommand, "tool", "buf", "generate", m.RepositoryRoot,
		"--config", filepath.Join(m.RepositoryRoot, "buf.yaml"),
		"--template", filepath.Join(m.RepositoryRoot, target.Template),
		"--output", destination,
	)
	command.Dir = filepath.Join(m.RepositoryRoot, "tools", "protobuf")
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	return command.Run()
}

func (m *Manager) ensureTypeScriptGenerator() error {
	if executable := os.Getenv("KENT_PROTOBUF_TS_GENERATOR"); executable != "" {
		return ensureExecutable(executable)
	}
	executable := filepath.Join(m.RepositoryRoot, "tools", "protobuf", "node_modules", ".bin", "protoc-gen-es")
	if _, err := os.Stat(executable); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	command := exec.Command("pnpm", "install", "--frozen-lockfile")
	command.Dir = filepath.Join(m.RepositoryRoot, "tools", "protobuf")
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	if err := command.Run(); err != nil {
		return fmt.Errorf("install TypeScript Protobuf generator: %w", err)
	}
	return nil
}

func ensureExecutable(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if info.Mode()&0o111 == 0 {
		return fmt.Errorf("%s is not executable", path)
	}
	return nil
}

func (m *Manager) inputFingerprint(target Target) (string, error) {
	inputs := []string{
		"api/proto",
		"buf.yaml",
		"buf.lock",
		target.Template,
		"tools/protobuf/go.mod",
		"tools/protobuf/go.sum",
		"tools/protobuf/internal/registrygen",
	}
	inputs = append(inputs, target.Inputs...)
	hasher := sha256.New()
	_, _ = io.WriteString(hasher, fingerprintVersion+"\x00"+target.Name+"\x00")
	for _, input := range inputs {
		if err := hashPath(hasher, m.RepositoryRoot, input); err != nil {
			return "", err
		}
	}
	return hex.EncodeToString(hasher.Sum(nil)), nil
}

func (m *Manager) manifestPath(target Target) string {
	return filepath.Join(m.RepositoryRoot, ".generated", "protobuf", target.Name+".json")
}

func (m *Manager) writeManifest(target Target, value manifest) error {
	path := m.manifestPath(target)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	content, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	content = append(content, '\n')
	temporary, err := os.CreateTemp(filepath.Dir(path), target.Name+"-manifest-")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if _, err := temporary.Write(content); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Chmod(temporaryPath, 0o644); err != nil {
		return err
	}
	return os.Rename(temporaryPath, path)
}

func (m *Manager) withLock(action func() error) error {
	lockPath := filepath.Join(m.RepositoryRoot, ".generated", "protobuf", "generation.lock")
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o755); err != nil {
		return err
	}
	fileLock := flock.New(lockPath)
	lockContext, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	locked, err := fileLock.TryLockContext(lockContext, 50*time.Millisecond)
	if err != nil {
		return err
	}
	if !locked {
		return fmt.Errorf("timed out waiting for Protobuf generation lock")
	}
	defer fileLock.Unlock()
	return action()
}

func replaceDirectory(source, destination string) error {
	if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
		return err
	}
	backup := fmt.Sprintf("%s.previous-%d", destination, time.Now().UnixNano())
	if err := os.RemoveAll(backup); err != nil {
		return err
	}
	destinationExists := true
	if err := os.Rename(destination, backup); errors.Is(err, os.ErrNotExist) {
		destinationExists = false
	} else if err != nil {
		return err
	}
	if err := os.Rename(source, destination); err != nil {
		if destinationExists {
			_ = os.Rename(backup, destination)
		}
		return err
	}
	if destinationExists {
		if err := os.RemoveAll(backup); err != nil {
			_ = os.RemoveAll(destination)
			_ = os.Rename(backup, destination)
			return err
		}
	}
	return nil
}

func fingerprintTree(root string) (string, error) {
	hasher := sha256.New()
	if err := hashPath(hasher, filepath.Dir(root), filepath.Base(root)); err != nil {
		return "", err
	}
	return hex.EncodeToString(hasher.Sum(nil)), nil
}

func hashPath(hasher io.Writer, root, relative string) error {
	path := filepath.Join(root, relative)
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return hashFile(hasher, root, path)
	}
	var paths []string
	err = filepath.WalkDir(path, func(candidate string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		paths = append(paths, candidate)
		return nil
	})
	if err != nil {
		return err
	}
	sort.Strings(paths)
	for _, candidate := range paths {
		if err := hashFile(hasher, root, candidate); err != nil {
			return err
		}
	}
	return nil
}

func hashFile(hasher io.Writer, root, path string) error {
	relative, err := filepath.Rel(root, path)
	if err != nil {
		return err
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	_, _ = io.WriteString(hasher, filepath.ToSlash(relative)+"\x00")
	_, err = hasher.Write(content)
	if err == nil {
		_, _ = io.WriteString(hasher, "\x00")
	}
	return err
}

func ResolveTargets(name string) ([]Target, error) {
	if name == "all" {
		return []Target{Targets["go"], Targets["ts"]}, nil
	}
	target, exists := Targets[strings.ToLower(name)]
	if !exists {
		return nil, fmt.Errorf("unknown Protobuf generation target %q", name)
	}
	return []Target{target}, nil
}
