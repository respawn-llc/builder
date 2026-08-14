package main

import (
	"context"
	"core/shared/config"
	"core/shared/protocol"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	serviceDisplayName        = config.ServiceDisplayName
	serviceLaunchdLabel       = config.ServiceLaunchdLabel
	serviceSystemdUnitName    = config.ServiceSystemdUnitName
	serviceWindowsServiceName = config.ServiceWindowsServiceName
	serviceLogDirName         = "logs"
	serviceStdoutLogName      = "server.log"
	serviceStderrLogName      = "server.err.log"
)

type serviceSpec struct {
	Config        config.App
	Executable    string
	Arguments     []string
	LogDir        string
	StdoutLogPath string
	StderrLogPath string
	Endpoint      string
}

type serviceStatus struct {
	Backend      string   `json:"backend"`
	Installed    bool     `json:"installed"`
	Loaded       bool     `json:"loaded"`
	Running      bool     `json:"running"`
	PID          int      `json:"pid,omitempty"`
	Command      []string `json:"command,omitempty"`
	Endpoint     string   `json:"endpoint"`
	Logs         []string `json:"logs"`
	InstallPath  string   `json:"install_path,omitempty"`
	Detail       string   `json:"detail,omitempty"`
	Hints        []string `json:"hints,omitempty"`
	HealthStatus string   `json:"health_status,omitempty"`
	HealthPID    int      `json:"health_pid,omitempty"`
}

type serviceBackend interface {
	Name() string
	Install(ctx context.Context, spec serviceSpec, force bool, start bool) error
	Uninstall(ctx context.Context, spec serviceSpec, stop bool) error
	Start(ctx context.Context, spec serviceSpec) error
	Stop(ctx context.Context, spec serviceSpec) error
	Restart(ctx context.Context, spec serviceSpec) error
	Status(ctx context.Context, spec serviceSpec) (serviceStatus, error)
}

type serviceCommandResult struct {
	Stdout string
	Stderr string
	Code   int
}

type serviceCommandError struct {
	Name   string
	Args   []string
	Result serviceCommandResult
}

func (e serviceCommandError) Error() string {
	detail := strings.TrimSpace(strings.Join([]string{e.Result.Stdout, e.Result.Stderr}, "\n"))
	if detail == "" {
		detail = fmt.Sprintf("exit code %d", e.Result.Code)
	}
	return fmt.Sprintf("%s %s failed: %s", e.Name, strings.Join(e.Args, " "), detail)
}

var runServiceCommand = func(ctx context.Context, name string, args ...string) (serviceCommandResult, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	configureNoWindow(cmd)
	stdout, err := cmd.Output()
	result := serviceCommandResult{Stdout: string(stdout)}
	if err == nil {
		return result, nil
	}
	if exitErr, ok := err.(*exec.ExitError); ok {
		result.Stderr = string(exitErr.Stderr)
		result.Code = exitErr.ExitCode()
		return result, serviceCommandError{Name: name, Args: args, Result: result}
	}
	result.Stderr = err.Error()
	result.Code = 1
	return result, serviceCommandError{Name: name, Args: args, Result: result}
}

var serviceHTTPClient = &http.Client{Timeout: 500 * time.Millisecond}
var resolveServiceExecutablePath = defaultServiceExecutablePath
var loadServiceSpec = defaultLoadServiceSpec
var serviceBackendFactory = currentServiceBackend

func defaultLoadServiceSpec() (serviceSpec, error) {
	cfg, err := config.LoadGlobal(config.LoadOptions{})
	if err != nil {
		return serviceSpec{}, err
	}
	executable, err := resolveServiceExecutablePath()
	if err != nil {
		return serviceSpec{}, err
	}
	logDir := filepath.Join(cfg.PersistenceRoot, serviceLogDirName)
	return serviceSpec{
		Config:        cfg,
		Executable:    executable,
		Arguments:     serviceServeArguments(cfg.PersistenceRoot),
		LogDir:        logDir,
		StdoutLogPath: filepath.Join(logDir, serviceStdoutLogName),
		StderrLogPath: filepath.Join(logDir, serviceStderrLogName),
		Endpoint:      config.ServerHTTPBaseURL(cfg),
	}, nil
}

func serviceServeArguments(persistenceRoot string) []string {
	args := []string{"serve"}
	if trimmed := strings.TrimSpace(persistenceRoot); trimmed != "" {
		args = append(args, "--persistence-root", trimmed)
	}
	return args
}

func defaultServiceExecutablePath() (string, error) {
	raw := strings.TrimSpace(os.Args[0])
	if raw == "" {
		return "", errors.New("resolve executable path: argv[0] is empty")
	}
	if strings.ContainsAny(raw, `/\`) {
		abs, err := filepath.Abs(raw)
		if err != nil {
			return "", fmt.Errorf("resolve executable path: %w", err)
		}
		return abs, nil
	}
	path, err := exec.LookPath(raw)
	if err != nil {
		return "", fmt.Errorf("resolve executable path: %w", err)
	}
	return path, nil
}

func ensureServiceLogDir(spec serviceSpec) error {
	if err := os.MkdirAll(spec.LogDir, 0o755); err != nil {
		return fmt.Errorf("create service log dir: %w", err)
	}
	return nil
}

func serviceCommand(spec serviceSpec) []string {
	cmd := make([]string, 0, 1+len(spec.Arguments))
	cmd = append(cmd, spec.Executable)
	cmd = append(cmd, spec.Arguments...)
	return cmd
}

func commandString(args []string) string {
	if len(args) == 0 {
		return ""
	}
	parts := make([]string, 0, len(args))
	for _, arg := range args {
		parts = append(parts, shellQuote(arg))
	}
	return strings.Join(parts, " ")
}

func commandArgsEqual(left []string, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

func shellQuote(value string) string {
	if value == "" {
		return "''"
	}
	if !strings.ContainsAny(value, " \t\n'\"\\$`!*?[]{}();&|<>") {
		return value
	}
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}

func shellCommand(tokens ...string) string {
	return commandString(tokens)
}

func applyHealthProbe(ctx context.Context, status serviceStatus, spec serviceSpec) serviceStatus {
	healthStatus, pid := probeServiceHealth(ctx, spec)
	if strings.TrimSpace(healthStatus) == "" {
		return status
	}
	status.HealthStatus = healthStatus
	if pid > 0 {
		status.HealthPID = pid
	}
	return status
}

func probeServiceHealth(ctx context.Context, spec serviceSpec) (string, int) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, spec.Endpoint+protocol.HealthPath, nil)
	if err != nil {
		return "", 0
	}
	resp, err := serviceHTTPClient.Do(req)
	if err != nil {
		return "", 0
	}
	defer func() { _ = resp.Body.Close() }()
	type healthResponse struct {
		Status string `json:"status"`
		PID    int    `json:"pid"`
	}
	var health healthResponse
	if err := json.NewDecoder(resp.Body).Decode(&health); err != nil {
		return "", 0
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", 0
	}
	return strings.TrimSpace(health.Status), health.PID
}

func parsePositiveInt(value string) int {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return 0
	}
	parsed, err := strconv.Atoi(trimmed)
	if err != nil || parsed <= 0 {
		return 0
	}
	return parsed
}
