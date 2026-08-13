package main

import (
	"fmt"
	"os"

	"core/shared/apicontract/internal/migrationcheck"
)

func main() {
	if err := migrationcheck.CheckExecutionTarget(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
