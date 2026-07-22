package main

import (
	"os"
)

func main() {
	if len(os.Args) < 2 {
		os.Exit(2)
	}
	if err := runMode(os.Args[1]); err != nil {
		os.Exit(2)
	}
}
