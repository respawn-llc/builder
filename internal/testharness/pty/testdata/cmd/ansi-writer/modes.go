package main

import (
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"golang.org/x/term"
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
	case "read-large":
		state, err := term.MakeRaw(int(os.Stdin.Fd()))
		if err != nil {
			return fmt.Errorf("make stdin raw: %w", err)
		}
		defer func() { _ = term.Restore(int(os.Stdin.Fd()), state) }()
		fmt.Print("\x1b[?25h")
		const size = 256 * 1024
		payload := make([]byte, size)
		if _, err := io.ReadFull(os.Stdin, payload); err != nil {
			return fmt.Errorf("read large input: %w", err)
		}
		fmt.Printf("received:%d", len(payload))
	case "hang":
		fmt.Print("ready")
		time.Sleep(24 * time.Hour)
	case "ignore-term":
		signal.Ignore(syscall.SIGHUP, syscall.SIGTERM)
		fmt.Print("\x1b[?25h")
		time.Sleep(24 * time.Hour)
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
