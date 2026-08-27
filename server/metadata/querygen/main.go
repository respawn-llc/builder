package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"core/server/metadata"
)

const (
	metadataQuerySourceDirectory     = "server/metadata/querysrc"
	renderedQueriesPath              = "server/metadata/queries.sql"
	sqlcConfigPath                   = "sqlc.yaml"
	generatedQueriesDirectory        = "server/metadata/sqlitegen"
	generatedQueriesFilename         = "queries.sql.go"
	generatedPageDescriptorsFilename = "task_search_page_descriptors_generated.go"
	generatedSchemaContractFilename  = "task_search_schema_contract_generated.go"
)

func main() {
	if err := runCommand(os.Args[1:]); err != nil {
		exitWithError(err)
	}
}

func runCommand(args []string) error {
	if len(args) == 0 {
		return errors.New("command is required: generate, generate-normalization, or check-normalization")
	}
	switch args[0] {
	case "generate":
		return generateMetadataQueriesCommand(args[1:])
	case "generate-normalization":
		return runNormalizationCommand("generate", args[1:])
	case "check-normalization":
		return runNormalizationCommand("check", args[1:])
	default:
		return fmt.Errorf(
			"unknown command %q: expected generate, generate-normalization, or check-normalization",
			args[0],
		)
	}
}

func generateMetadataQueriesCommand(args []string) (err error) {
	if len(args) != 0 {
		return errors.New("generate does not accept positional arguments")
	}
	repositoryRoot, err := findModuleRoot()
	if err != nil {
		return err
	}
	renderer, err := metadata.LoadQuerySourceRenderer(filepath.Join(repositoryRoot, metadataQuerySourceDirectory))
	if err != nil {
		return err
	}
	generatedDir := filepath.Join(repositoryRoot, generatedQueriesDirectory)
	renderedPath := filepath.Join(repositoryRoot, renderedQueriesPath)
	if err := os.MkdirAll(generatedDir, 0o755); err != nil {
		return fmt.Errorf("create generated query directory: %w", err)
	}
	rendered, err := renderer.Render()
	if err != nil {
		return err
	}
	if err := os.WriteFile(renderedPath, rendered, 0o600); err != nil {
		return fmt.Errorf("write rendered metadata queries: %w", err)
	}
	defer func() {
		if cleanupErr := os.Remove(renderedPath); cleanupErr != nil && !errors.Is(cleanupErr, os.ErrNotExist) {
			err = errors.Join(err, fmt.Errorf("remove rendered metadata queries: %w", cleanupErr))
		}
	}()
	command := exec.Command("sqlc", "generate", "-f", filepath.Join(repositoryRoot, sqlcConfigPath))
	command.Dir = repositoryRoot
	output, commandErr := command.CombinedOutput()
	if commandErr != nil {
		return fmt.Errorf(
			"generate metadata SQL adapters: %w\n%s",
			commandErr,
			strings.TrimSpace(string(output)),
		)
	}
	if err := annotateFile(filepath.Join(generatedDir, generatedQueriesFilename)); err != nil {
		return err
	}
	pageQuery, err := renderer.RenderTaskSearchPageDescriptors()
	if err != nil {
		return err
	}
	pageAdapter, err := generateTaskSearchPageDescriptors(pageQuery)
	if err != nil {
		return err
	}
	if err := writeGeneratedFile(
		filepath.Join(generatedDir, generatedPageDescriptorsFilename),
		pageAdapter,
	); err != nil {
		return err
	}
	contractQuery, err := renderer.RenderTaskSearchSchemaContract()
	if err != nil {
		return err
	}
	contractAdapter, err := generateTaskSearchSchemaContract(contractQuery)
	if err != nil {
		return err
	}
	if err := writeGeneratedFile(
		filepath.Join(generatedDir, generatedSchemaContractFilename),
		contractAdapter,
	); err != nil {
		return err
	}
	return nil
}

func writeGeneratedFile(path string, source []byte) error {
	if err := os.WriteFile(path, source, 0o644); err != nil {
		return fmt.Errorf("write generated query adapter %s: %w", filepath.Base(path), err)
	}
	return nil
}

func exitWithError(err error) {
	_, _ = fmt.Fprintf(os.Stderr, "metadataquerygen: %v\n", err)
	os.Exit(1)
}
