//go:build linux

package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	brand "core/shared/config"
)

type systemdServiceBackend struct{}

func currentServiceBackend() serviceBackend {
	return systemdServiceBackend{}
}

func (systemdServiceBackend) Name() string {
	return "systemd"
}

func (systemdServiceBackend) Install(ctx context.Context, spec serviceSpec, force bool, start bool) error {
	if err := ensureServiceLogDir(spec); err != nil {
		return err
	}
	path, err := systemdUnitPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create systemd user dir: %w", err)
	}
	if !force {
		if existing, err := os.ReadFile(path); err == nil && strings.TrimSpace(string(existing)) != strings.TrimSpace(renderSystemdUnit(spec)) {
			return fmt.Errorf(brand.ServiceDisplayName+" is already installed at %s; use --force to rewrite it", path)
		}
	}
	previousUnit, previousErr := os.ReadFile(path)
	previousExists := previousErr == nil
	if err := os.WriteFile(path, []byte(renderSystemdUnit(spec)), 0o644); err != nil {
		return fmt.Errorf("write systemd unit: %w", err)
	}
	installed := false
	defer func() {
		if installed {
			return
		}
		if previousExists {
			_ = os.WriteFile(path, previousUnit, 0o644)
		} else {
			_ = os.Remove(path)
		}
		_, _ = runServiceCommand(ctx, "systemctl", "--user", "daemon-reload")
	}()
	if _, err := runServiceCommand(ctx, "systemctl", "--user", "daemon-reload"); err != nil {
		return err
	}
	if _, err := runServiceCommand(ctx, "systemctl", "--user", "enable", serviceSystemdUnitName); err != nil {
		return err
	}
	if start {
		if _, err := runServiceCommand(ctx, "systemctl", "--user", "restart", serviceSystemdUnitName); err != nil {
			return err
		}
	}
	installed = true
	return nil
}

func (systemdServiceBackend) Uninstall(ctx context.Context, spec serviceSpec, stop bool) error {
	if stop {
		_ = systemdServiceBackend{}.Stop(ctx, spec)
	}
	_, _ = runServiceCommand(ctx, "systemctl", "--user", "disable", serviceSystemdUnitName)
	path, err := systemdUnitPath()
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove systemd unit: %w", err)
	}
	_, _ = runServiceCommand(ctx, "systemctl", "--user", "daemon-reload")
	return nil
}

func (systemdServiceBackend) Start(ctx context.Context, spec serviceSpec) error {
	if err := requireSystemdUnitInstalled(); err != nil {
		return err
	}
	_, err := runServiceCommand(ctx, "systemctl", "--user", "start", serviceSystemdUnitName)
	return err
}

func (systemdServiceBackend) Stop(ctx context.Context, spec serviceSpec) error {
	loadState, err := systemdLoadState(ctx)
	if err != nil || loadState != "loaded" {
		return nil
	}
	_, err = runServiceCommand(ctx, "systemctl", "--user", "stop", serviceSystemdUnitName)
	return err
}

func (systemdServiceBackend) Restart(ctx context.Context, spec serviceSpec) error {
	if err := requireSystemdUnitInstalled(); err != nil {
		return err
	}
	_, err := runServiceCommand(ctx, "systemctl", "--user", "restart", serviceSystemdUnitName)
	return err
}

func (systemdServiceBackend) Status(ctx context.Context, spec serviceSpec) (serviceStatus, error) {
	path, err := systemdUnitPath()
	if err != nil {
		return serviceStatus{}, err
	}
	installed := false
	if _, err := os.Stat(path); err == nil {
		installed = true
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return serviceStatus{}, fmt.Errorf("stat systemd unit: %w", err)
	}
	activeResult, _ := runServiceCommand(ctx, "systemctl", "--user", "is-active", serviceSystemdUnitName)
	running := strings.TrimSpace(activeResult.Stdout) == "active"
	loadState, _ := systemdLoadState(ctx)
	loaded := loadState == "loaded"
	showResult, _ := runServiceCommand(ctx, "systemctl", "--user", "show", serviceSystemdUnitName, "--property=MainPID", "--value")
	pid := parsePositiveInt(showResult.Stdout)
	return serviceStatus{
		Backend:     "systemd",
		Installed:   installed,
		Loaded:      loaded,
		Running:     running,
		PID:         pid,
		Command:     readSystemdRegisteredCommand(path),
		Endpoint:    spec.Endpoint,
		Logs:        []string{spec.StdoutLogPath, spec.StderrLogPath},
		InstallPath: path,
		Hints: []string{
			"On headless Linux, run `loginctl enable-linger $USER` if the service should survive logout.",
		},
	}, nil
}

func requireSystemdUnitInstalled() error {
	path, err := systemdUnitPath()
	if err != nil {
		return err
	}
	if _, err := os.Stat(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return errors.New(brand.ServiceDisplayName + " is not installed; run `" + brand.Command + " service install`")
		}
		return fmt.Errorf("stat systemd unit: %w", err)
	}
	return nil
}

func systemdLoadState(ctx context.Context) (string, error) {
	result, err := runServiceCommand(ctx, "systemctl", "--user", "show", serviceSystemdUnitName, "--property=LoadState", "--value")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(result.Stdout), nil
}

func readSystemdRegisteredCommand(path string) []string {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	for _, rawLine := range strings.Split(string(data), "\n") {
		line := strings.TrimSpace(rawLine)
		if value, ok := strings.CutPrefix(line, "ExecStart="); ok {
			return parseSystemdCommand(value)
		}
	}
	return nil
}

func parseSystemdCommand(value string) []string {
	args := []string{}
	var builder strings.Builder
	inQuote := false
	escaped := false
	percentPending := false
	flush := func() {
		if builder.Len() == 0 {
			return
		}
		args = append(args, builder.String())
		builder.Reset()
	}
	for _, r := range value {
		if escaped {
			switch r {
			case 'n':
				builder.WriteByte('\n')
			default:
				builder.WriteRune(r)
			}
			escaped = false
			continue
		}
		if percentPending {
			// systemd writes a literal percent as `%%`; collapse the pair. A lone
			// `%` (a specifier in units we did not author) is preserved verbatim.
			percentPending = false
			if r == '%' {
				builder.WriteByte('%')
				continue
			}
			builder.WriteByte('%')
		}
		switch r {
		case '%':
			percentPending = true
		case '\\':
			escaped = true
		case '"':
			inQuote = !inQuote
		case ' ', '\t':
			if inQuote {
				builder.WriteRune(r)
			} else {
				flush()
			}
		default:
			builder.WriteRune(r)
		}
	}
	if escaped {
		builder.WriteByte('\\')
	}
	if percentPending {
		builder.WriteByte('%')
	}
	flush()
	return args
}

func systemdUnitPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home dir: %w", err)
	}
	return filepath.Join(home, ".config", "systemd", "user", serviceSystemdUnitName), nil
}

func renderSystemdUnit(spec serviceSpec) string {
	lines := []string{
		"[Unit]",
		"Description=" + brand.Product + " server background service",
		"After=network-online.target",
		"",
		"[Service]",
		"Type=simple",
		"ExecStart=" + systemdCommand(serviceCommand(spec)),
		"Restart=always",
		"RestartSec=2",
		"StandardOutput=append:" + systemdEscapeSpecifiers(spec.StdoutLogPath),
		"StandardError=append:" + systemdEscapeSpecifiers(spec.StderrLogPath),
		"",
		"[Install]",
		"WantedBy=default.target",
		"",
	}
	return strings.Join(lines, "\n")
}

func systemdCommand(args []string) string {
	quoted := make([]string, 0, len(args))
	for _, arg := range args {
		quoted = append(quoted, systemdQuote(arg))
	}
	return strings.Join(quoted, " ")
}

// systemdEscapeSpecifiers doubles `%` so systemd writes it as a literal rather
// than expanding it as a unit specifier (systemd.unit(5)/systemd.service(5)).
// This applies to every operator-controlled value rendered into a setting that
// systemd specifier-expands (ExecStart arguments and the StandardOutput/Error
// paths), independent of shell-style quoting.
func systemdEscapeSpecifiers(value string) string {
	return strings.ReplaceAll(value, "%", "%%")
}

func systemdQuote(value string) string {
	if value == "" {
		return `""`
	}
	value = systemdEscapeSpecifiers(value)
	if !strings.ContainsAny(value, " \t\n\"\\$`") {
		return value
	}
	var builder strings.Builder
	builder.WriteByte('"')
	for _, r := range value {
		switch r {
		case '"', '\\', '$', '`':
			builder.WriteByte('\\')
			builder.WriteRune(r)
		case '\n':
			builder.WriteString(`\n`)
		default:
			builder.WriteRune(r)
		}
	}
	builder.WriteByte('"')
	return builder.String()
}
