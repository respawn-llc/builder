package imagefileio

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"
)

func TestMain(m *testing.M) {
	if os.Getenv("KENT_TEST_BLOCK_IMAGE_FILE_READER") == "1" {
		for {
			time.Sleep(time.Hour)
		}
	}
	ExitIfWorker(os.Args[1:], os.Stdin, os.Stdout, os.Stderr)
	os.Exit(m.Run())
}

func TestReadTimesOutBlockedWorker(t *testing.T) {
	t.Setenv("KENT_TEST_BLOCK_IMAGE_FILE_READER", "1")

	_, err := readWithTimeout(context.Background(), "blocked.png", MaxReadBytes, 50*time.Millisecond)
	if !errors.Is(err, errReadTimeout) {
		t.Fatalf("blocked image worker error = %v, want file-read timeout", err)
	}
}
