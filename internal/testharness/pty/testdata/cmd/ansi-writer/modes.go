package main

import (
	"fmt"
	"io"
	"os"
	"time"
)

func runMode(mode string) error {
	switch mode {
	case "no-output":
		return nil
	case "write":
		fmt.Print("hello")
	case "echo-byte":
		var b [1]byte
		if _, err := io.ReadFull(os.Stdin, b[:]); err != nil {
			fmt.Fprintf(os.Stderr, "read input: %v", err)
			os.Exit(1)
		}
		fmt.Printf("input:%s", string(b[:]))
	case "hang":
		fmt.Print("ready")
		select {}
	case "resize-order":
		fmt.Print("before")
		time.Sleep(500 * time.Millisecond)
		fmt.Print("\x1b[3;1Hafter")
	case "resize-before-write":
		time.Sleep(100 * time.Millisecond)
		fmt.Print("\x1b[3;1Hafter")
	default:
		return fmt.Errorf("unknown mode %q", mode)
	}
	return nil
}
