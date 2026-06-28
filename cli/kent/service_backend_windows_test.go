//go:build windows

package main

import (
	"core/shared/config"
	"path/filepath"
	"testing"
)

func TestWindowsServiceRunArgumentsBakesRoot(t *testing.T) {
	got := windowsServiceRunArguments(`C:\Roots\kent`)
	want := []string{"service", "run", "--persistence-root", `C:\Roots\kent`}
	if !commandArgsEqual(got, want) {
		t.Fatalf("windowsServiceRunArguments = %#v, want %#v", got, want)
	}
}

func TestWindowsServiceRunArgumentsOmitsEmptyRoot(t *testing.T) {
	got := windowsServiceRunArguments("   ")
	want := []string{"service", "run"}
	if !commandArgsEqual(got, want) {
		t.Fatalf("windowsServiceRunArguments = %#v, want %#v", got, want)
	}
}

func TestWindowsServerPIDPathUnderServiceDir(t *testing.T) {
	spec := serviceSpec{Config: config.App{PersistenceRoot: `C:\Roots\kent`}}
	got := windowsServerPIDPath(spec)
	want := filepath.Join(`C:\Roots\kent`, "service", "server.pid")
	if got != want {
		t.Fatalf("windowsServerPIDPath = %q, want %q", got, want)
	}
}
