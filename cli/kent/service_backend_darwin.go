//go:build darwin

package main

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	brand "core/shared/config"
)

// Sentinel launchd reload/restart errors. Dynamic diagnostic detail (pids,
// endpoints, launchd state) is attached via %w so callers can match the
// failure mode with errors.Is without comparing rendered message text.
var (
	errLaunchdServerNotHealthy       = errors.New("restarted launchd job, but " + brand.Product + " server did not become healthy before timeout")
	errLaunchdServerProcessNotExited = errors.New("running " + brand.Product + " server process did not exit before service restart")
)

type launchdServiceBackend struct{}

var launchdServiceShutdownTimeout = 5 * time.Second
var launchdServiceShutdownPollInterval = 100 * time.Millisecond
var signalLaunchdServiceProcess = func(pid int) error {
	process, err := os.FindProcess(pid)
	if err != nil {
		return fmt.Errorf("find running "+brand.Product+" server process %d: %w", pid, err)
	}
	if err := process.Signal(syscall.SIGTERM); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return fmt.Errorf("stop running "+brand.Product+" server process %d before service restart: %w", pid, err)
	}
	return nil
}
var killLaunchdServiceProcess = func(pid int) error {
	if err := syscall.Kill(pid, syscall.SIGKILL); err != nil && err != syscall.ESRCH {
		return fmt.Errorf("force stop running "+brand.Product+" server process %d before service restart: %w", pid, err)
	}
	return nil
}
var launchdServiceProcessAlive = func(pid int) (bool, error) {
	if err := syscall.Kill(pid, 0); err != nil {
		if err == syscall.ESRCH {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

func currentServiceBackend() serviceBackend {
	return launchdServiceBackend{}
}

func (launchdServiceBackend) Name() string {
	return "launchd"
}

func (launchdServiceBackend) Install(ctx context.Context, spec serviceSpec, force bool, start bool) error {
	path, err := writeLaunchdServicePlist(spec, force)
	if err != nil {
		return err
	}
	if start {
		if err := reloadLaunchdService(ctx, spec, path); err != nil {
			return err
		}
	}
	return nil
}

func writeLaunchdServicePlist(spec serviceSpec, force bool) (string, error) {
	if err := ensureServiceLogDir(spec); err != nil {
		return "", err
	}
	path, err := launchdPlistPath()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", fmt.Errorf("create LaunchAgents dir: %w", err)
	}
	rendered := []byte(renderLaunchdPlist(spec))
	if !force {
		existing, err := os.ReadFile(path)
		switch {
		case err == nil:
			if !bytes.Equal(existing, rendered) {
				return "", fmt.Errorf(brand.ServiceDisplayName+" is already installed at %s; use --force to rewrite it", path)
			}
		case errors.Is(err, os.ErrNotExist):
		default:
			return "", fmt.Errorf("read launchd plist: %w", err)
		}
	}
	if err := os.WriteFile(path, rendered, 0o644); err != nil {
		return "", fmt.Errorf("write launchd plist: %w", err)
	}
	return path, nil
}

func (launchdServiceBackend) Uninstall(ctx context.Context, spec serviceSpec, stop bool) error {
	if stop {
		if err := (launchdServiceBackend{}).Stop(ctx, spec); err != nil {
			return err
		}
	}
	path, err := launchdPlistPath()
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove launchd plist: %w", err)
	}
	return nil
}

func (launchdServiceBackend) Start(ctx context.Context, spec serviceSpec) error {
	path, err := launchdPlistPath()
	if err != nil {
		return err
	}
	if _, err := os.Stat(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return errors.New(brand.ServiceDisplayName + " is not installed; run `" + brand.Command + " service install`")
		}
		return fmt.Errorf("stat launchd plist: %w", err)
	}
	if loaded, _ := launchdLoaded(ctx); !loaded {
		if err := bootstrapLaunchdService(ctx, spec, path); err != nil {
			return err
		}
		return waitForLaunchdServiceStartup(ctx, spec)
	}
	topology, err := resolveDarwinServiceTopology(ctx, spec)
	if err != nil {
		return err
	}
	if _, err = runServiceCommand(ctx, "launchctl", "kickstart", "-k", fmt.Sprintf("gui/%d", os.Getuid())+"/"+serviceLaunchdLabel); err != nil {
		return err
	}
	if err := waitForCapturedDarwinTopologyShutdown(ctx, topology, false); err != nil {
		return err
	}
	return waitForLaunchdServiceStartup(ctx, spec)
}

func (launchdServiceBackend) Stop(ctx context.Context, spec serviceSpec) error {
	topology, err := resolveDarwinServiceTopology(ctx, spec)
	if err != nil {
		return err
	}
	if !topology.Host.Loaded {
		return nil
	}
	if _, err := runServiceCommand(ctx, "launchctl", "bootout", fmt.Sprintf("gui/%d", os.Getuid())+"/"+serviceLaunchdLabel); err != nil {
		return err
	}
	return waitForCapturedDarwinTopologyShutdown(ctx, topology, true)
}

func (launchdServiceBackend) Restart(ctx context.Context, spec serviceSpec) error {
	path, err := launchdPlistPath()
	if err != nil {
		return err
	}
	if _, err := os.Stat(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return errors.New(brand.ServiceDisplayName + " is not installed; run `" + brand.Command + " service install`")
		}
		return fmt.Errorf("stat launchd plist: %w", err)
	}
	path, err = writeLaunchdServicePlist(spec, true)
	if err != nil {
		return err
	}
	return reloadLaunchdService(ctx, spec, path)
}

func (launchdServiceBackend) Status(ctx context.Context, spec serviceSpec) (serviceStatus, error) {
	path, err := launchdPlistPath()
	if err != nil {
		return serviceStatus{}, err
	}
	installed := false
	if _, err := os.Stat(path); err == nil {
		installed = true
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return serviceStatus{}, fmt.Errorf("stat launchd plist: %w", err)
	}
	topology, topologyErr := resolveDarwinServiceTopology(ctx, spec)
	if topologyErr != nil {
		return serviceStatus{}, topologyErr
	}
	command := serverChildCommand(spec)
	pid := 0
	if topology.Child != nil {
		pid = topology.Child.PID
		command = topology.Child.Command
	}
	return serviceStatus{
		Backend:     "launchd",
		Installed:   installed,
		Loaded:      topology.Host.Loaded,
		Running:     topology.Host.PID > 0 || topology.Host.State == "running",
		PID:         pid,
		Command:     command,
		Endpoint:    spec.Endpoint,
		Logs:        []string{spec.StdoutLogPath, spec.StderrLogPath},
		InstallPath: path,
	}, nil
}

func reloadLaunchdService(ctx context.Context, spec serviceSpec, path string) error {
	if topology, err := resolveDarwinServiceTopology(ctx, spec); err != nil {
		return err
	} else if topology.Host.Loaded {
		if _, err := runServiceCommand(ctx, "launchctl", "bootout", fmt.Sprintf("gui/%d", os.Getuid())+"/"+serviceLaunchdLabel); err != nil {
			return err
		}
		if err := waitForCapturedDarwinTopologyShutdown(ctx, topology, true); err != nil {
			return err
		}
	} else {
		lockFree, err := darwinServiceLockAvailable(spec)
		if err != nil {
			return err
		}
		if !lockFree {
			return errors.New("a prior Darwin service activation still owns the server lock; refusing to bootstrap an overlapping server")
		}
		healthStatus, healthPID := probeServiceHealth(ctx, spec)
		if healthStatus == "ok" {
			return fmt.Errorf(brand.Product+" server is already running on %s (pid %d), but launchd has no proven host for it", spec.Endpoint, healthPID)
		}
	}
	if err := bootstrapLaunchdService(ctx, spec, path); err != nil {
		return err
	}
	return waitForLaunchdServiceStartup(ctx, spec)
}

func darwinServiceLockAvailable(spec serviceSpec) (bool, error) {
	path := darwinServiceLockPath(spec.Config.PersistenceRoot)
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return false, fmt.Errorf("open Darwin service activation lock: %w", err)
	}
	defer file.Close()
	err = syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	switch {
	case err == nil:
		_ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
		return true, nil
	case errors.Is(err, syscall.EWOULDBLOCK):
		return false, nil
	default:
		return false, fmt.Errorf("inspect Darwin service activation lock: %w", err)
	}
}

func waitForLaunchdServiceProcessExit(ctx context.Context, pid int) error {
	timeout := launchdServiceShutdownTimeout
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	interval := launchdServiceShutdownPollInterval
	if interval <= 0 {
		interval = 100 * time.Millisecond
	}
	deadline := time.Now().Add(timeout)
	for {
		alive, err := launchdServiceProcessAlive(pid)
		if err != nil {
			return fmt.Errorf("check running "+brand.Product+" server process %d before service restart: %w", pid, err)
		}
		if !alive {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("%w (pid %d)", errLaunchdServerProcessNotExited, pid)
		}
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func waitForLaunchdServiceStartup(ctx context.Context, spec serviceSpec) error {
	timeout := launchdServiceShutdownTimeout
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	interval := launchdServiceShutdownPollInterval
	if interval <= 0 {
		interval = 100 * time.Millisecond
	}
	deadline := time.Now().Add(timeout)
	lastDetail := ""
	for {
		if err := resolveDarwinServiceReadiness(ctx, spec); err == nil {
			return nil
		} else {
			lastDetail = err.Error()
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("%w: %s", errLaunchdServerNotHealthy, lastDetail)
		}
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func waitForCapturedDarwinTopologyShutdown(ctx context.Context, topology darwinServiceTopology, requireLaunchdRelease bool) error {
	timeout := launchdServiceShutdownTimeout
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	deadline := time.Now().Add(timeout)
	for {
		loaded := false
		if requireLaunchdRelease {
			loaded, _ = launchdLoaded(ctx)
		}
		hostAlive := false
		childAlive := false
		var err error
		if topology.Host.PID > 0 {
			hostAlive, err = launchdServiceProcessAlive(topology.Host.PID)
			if err != nil {
				return err
			}
		}
		if topology.Child != nil {
			childAlive, err = launchdServiceProcessAlive(topology.Child.PID)
			if err != nil {
				return err
			}
		}
		if !hostAlive && !childAlive && !loaded {
			return nil
		}
		if time.Now().After(deadline) {
			if childAlive && topology.Child != nil {
				if err := signalLaunchdServiceProcess(topology.Child.PID); err != nil {
					return err
				}
				if err := waitForLaunchdServiceProcessExit(ctx, topology.Child.PID); err != nil {
					if err := killLaunchdServiceProcess(topology.Child.PID); err != nil {
						return err
					}
					if err := waitForLaunchdServiceProcessExit(ctx, topology.Child.PID); err != nil {
						return err
					}
				}
			}
			if hostAlive {
				return fmt.Errorf("launchd host process %d did not exit", topology.Host.PID)
			}
			if loaded {
				return errors.New("launchd did not release the stopped service activation")
			}
			return nil
		}
		timer := time.NewTimer(launchdServiceShutdownPollInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func bootstrapLaunchdService(ctx context.Context, spec serviceSpec, path string) error {
	if _, err := runServiceCommand(ctx, "launchctl", "bootstrap", fmt.Sprintf("gui/%d", os.Getuid()), path); err != nil {
		return err
	}
	return nil
}

func readLaunchdRegisteredCommand(path string) []string {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	return parseLaunchdProgramArguments(data)
}

func parseLaunchdProgramArguments(data []byte) []string {
	decoder := xml.NewDecoder(bytes.NewReader(data))
	lastKey := ""
	inProgramArguments := false
	args := []string{}
	for {
		token, err := decoder.Token()
		if err != nil {
			return args
		}
		switch typed := token.(type) {
		case xml.StartElement:
			switch typed.Name.Local {
			case "key":
				lastKey = strings.TrimSpace(readXMLText(decoder, "key"))
			case "array":
				if lastKey == "ProgramArguments" {
					inProgramArguments = true
				}
			case "string":
				text := readXMLText(decoder, "string")
				if inProgramArguments {
					args = append(args, text)
				}
			}
		case xml.EndElement:
			if typed.Name.Local == "array" && inProgramArguments {
				return args
			}
		}
	}
}

func readXMLText(decoder *xml.Decoder, endElement string) string {
	var builder strings.Builder
	for {
		token, err := decoder.Token()
		if err != nil {
			return builder.String()
		}
		switch typed := token.(type) {
		case xml.CharData:
			builder.Write([]byte(typed))
		case xml.EndElement:
			if typed.Name.Local == endElement {
				return builder.String()
			}
		}
	}
}

func launchdPlistPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home dir: %w", err)
	}
	return filepath.Join(home, "Library", "LaunchAgents", serviceLaunchdLabel+".plist"), nil
}

func launchdLoaded(ctx context.Context) (bool, string) {
	result, err := runServiceCommand(ctx, "launchctl", "print", fmt.Sprintf("gui/%d", os.Getuid())+"/"+serviceLaunchdLabel)
	if err != nil {
		return false, strings.TrimSpace(strings.Join([]string{result.Stdout, result.Stderr}, "\n"))
	}
	return true, strings.TrimSpace(strings.Join([]string{result.Stdout, result.Stderr}, "\n"))
}

func launchdPID(output string) int {
	for _, line := range strings.Split(output, "\n") {
		parts := strings.SplitN(strings.TrimSpace(line), "=", 2)
		if len(parts) != 2 {
			continue
		}
		if strings.TrimSpace(parts[0]) == "pid" {
			return parsePositiveInt(parts[1])
		}
	}
	return 0
}

func launchdState(output string) string {
	for _, line := range strings.Split(output, "\n") {
		parts := strings.SplitN(strings.TrimSpace(line), "=", 2)
		if len(parts) != 2 {
			continue
		}
		if strings.TrimSpace(parts[0]) == "state" {
			return strings.TrimSpace(parts[1])
		}
	}
	return ""
}

func parseLaunchdPrintProgramArguments(output string) []string {
	args := []string{}
	inArguments := false
	for _, rawLine := range strings.Split(output, "\n") {
		line := strings.TrimSpace(rawLine)
		switch {
		case line == "arguments = {":
			inArguments = true
		case inArguments && line == "}":
			return args
		case inArguments && line != "":
			args = append(args, line)
		}
	}
	return nil
}

func renderLaunchdPlist(spec serviceSpec) string {
	var builder strings.Builder
	builder.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n")
	builder.WriteString(`<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">` + "\n")
	builder.WriteString("<plist version=\"1.0\">\n<dict>\n")
	writeLaunchdString(&builder, "Label", serviceLaunchdLabel)
	builder.WriteString("\t<key>ProgramArguments</key>\n\t<array>\n")
	for _, arg := range darwinServiceHostCommand(spec) {
		builder.WriteString("\t\t<string>")
		_ = xml.EscapeText(&builder, []byte(arg))
		builder.WriteString("</string>\n")
	}
	builder.WriteString("\t</array>\n")
	writeLaunchdBool(&builder, "RunAtLoad", true)
	builder.WriteString("\t<key>KeepAlive</key>\n\t<dict>\n")
	writeLaunchdBoolIndented(&builder, "SuccessfulExit", false)
	builder.WriteString("\t</dict>\n")
	writeLaunchdString(&builder, "StandardOutPath", spec.StdoutLogPath)
	writeLaunchdString(&builder, "StandardErrorPath", spec.StderrLogPath)
	builder.WriteString("</dict>\n</plist>\n")
	return builder.String()
}

func writeLaunchdString(builder *strings.Builder, key string, value string) {
	builder.WriteString("\t<key>")
	_ = xml.EscapeText(builder, []byte(key))
	builder.WriteString("</key>\n\t<string>")
	_ = xml.EscapeText(builder, []byte(value))
	builder.WriteString("</string>\n")
}

func writeLaunchdBool(builder *strings.Builder, key string, value bool) {
	builder.WriteString("\t<key>")
	_ = xml.EscapeText(builder, []byte(key))
	builder.WriteString("</key>\n")
	if value {
		builder.WriteString("\t<true/>\n")
	} else {
		builder.WriteString("\t<false/>\n")
	}
}

func writeLaunchdBoolIndented(builder *strings.Builder, key string, value bool) {
	builder.WriteString("\t\t<key>")
	_ = xml.EscapeText(builder, []byte(key))
	builder.WriteString("</key>\n")
	if value {
		builder.WriteString("\t\t<true/>\n")
	} else {
		builder.WriteString("\t\t<false/>\n")
	}
}
