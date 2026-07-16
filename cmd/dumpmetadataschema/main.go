// Command dumpmetadataschema prints the effective metadata SQLite schema after
// applying Kent's authoritative embedded migrations to an isolated database.
package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"core/server/metadata"
	"core/server/metadata/sqlitegen"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("dumpmetadataschema", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var usageWriteErr error
	fs.Usage = func() {
		_, usageWriteErr = fmt.Fprintln(stderr, "usage: dumpmetadataschema")
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			if usageWriteErr != nil {
				return 1
			}
			return 0
		}
		return 2
	}
	if fs.NArg() != 0 {
		if _, err := fmt.Fprintln(stderr, "dumpmetadataschema does not accept arguments"); err != nil {
			return 1
		}
		fs.Usage()
		if usageWriteErr != nil {
			return 1
		}
		return 2
	}

	dump, err := buildSchemaDump(context.Background())
	if err != nil {
		fmt.Fprintf(stderr, "dumpmetadataschema: %v\n", err)
		return 1
	}
	written, err := stdout.Write(dump)
	if err == nil && written != len(dump) {
		err = io.ErrShortWrite
	}
	if err != nil {
		fmt.Fprintf(stderr, "dumpmetadataschema: write stdout: %v\n", err)
		return 1
	}
	return 0
}

func buildSchemaDump(ctx context.Context) ([]byte, error) {
	root, err := os.MkdirTemp("", "kent-metadata-schema-")
	if err != nil {
		return nil, fmt.Errorf("create temporary metadata root: %w", err)
	}

	store, openErr := metadata.Open(root)
	if openErr != nil {
		return nil, errors.Join(
			fmt.Errorf("open temporary metadata store: %w", openErr),
			removeTemporaryRoot(root),
		)
	}

	dump, dumpErr := loadSchemaDump(ctx, store)
	err = errors.Join(
		dumpErr,
		wrapOperationalError("close temporary metadata store", store.Close()),
		removeTemporaryRoot(root),
	)
	if err != nil {
		return nil, err
	}
	return dump, nil
}

func loadSchemaDump(ctx context.Context, store *metadata.Store) ([]byte, error) {
	rows, err := store.Queries().ListMetadataSchemaDefinitions(ctx)
	if err != nil {
		return nil, fmt.Errorf("list metadata schema definitions: %w", err)
	}
	return renderSchemaDefinitions(rows)
}

type schemaObjectKind uint8

const (
	schemaObjectTable schemaObjectKind = iota
	schemaObjectView
	schemaObjectIndex
	schemaObjectTrigger
)

func renderSchemaDefinitions(rows []sqlitegen.ListMetadataSchemaDefinitionsRow) ([]byte, error) {
	var buffer bytes.Buffer
	for index, row := range rows {
		if _, err := parseSchemaObjectKind(row.ObjectKind); err != nil {
			return nil, fmt.Errorf("validate metadata schema object %q: %w", row.ObjectName, err)
		}
		if strings.TrimSpace(row.Ddl) == "" {
			return nil, fmt.Errorf("metadata schema object %q has empty DDL", row.ObjectName)
		}
		if index > 0 {
			buffer.WriteByte('\n')
		}
		buffer.WriteString(row.Ddl)
		buffer.WriteString(";\n")
	}
	return buffer.Bytes(), nil
}

func parseSchemaObjectKind(value string) (schemaObjectKind, error) {
	switch value {
	case "table":
		return schemaObjectTable, nil
	case "view":
		return schemaObjectView, nil
	case "index":
		return schemaObjectIndex, nil
	case "trigger":
		return schemaObjectTrigger, nil
	default:
		return 0, fmt.Errorf("unknown metadata schema object kind %q", value)
	}
}

func removeTemporaryRoot(root string) error {
	return wrapOperationalError("remove temporary metadata root", os.RemoveAll(root))
}

func wrapOperationalError(operation string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s: %w", operation, err)
}
