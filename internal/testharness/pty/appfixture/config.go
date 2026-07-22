package appfixture

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"core/server/metadata"
)

func PrepareConfigAndBinding(ctx context.Context, persistenceRoot string, workspaceRoot string) error {
	return PrepareConfigAndBindingWithOptions(ctx, persistenceRoot, workspaceRoot, ConfigOptions{})
}

type ConfigOptions struct {
	LifecycleHookCommand []string
}

func PrepareConfigAndBindingWithOptions(
	ctx context.Context,
	persistenceRoot string,
	workspaceRoot string,
	options ConfigOptions,
) error {
	if err := WriteConfigWithOptions(ctx, persistenceRoot, options); err != nil {
		return err
	}
	if _, err := metadata.RegisterBinding(ctx, persistenceRoot, workspaceRoot); err != nil {
		return fmt.Errorf("register fixture workspace binding: %w", err)
	}
	return nil
}

func WriteConfigWithOptions(ctx context.Context, persistenceRoot string, options ConfigOptions) error {
	if err := os.MkdirAll(persistenceRoot, 0o755); err != nil {
		return fmt.Errorf("create persistence root: %w", err)
	}
	port, err := freeTCPPort(ctx)
	if err != nil {
		return err
	}
	configPath := filepath.Join(persistenceRoot, "config.toml")
	var config strings.Builder
	fmt.Fprintf(
		&config,
		"model = \"gpt-5\"\nprovider_override = \"openai\"\nopenai_base_url = \"http://127.0.0.1:1/v1\"\nserver_port = %d\ntheme = \"dark\"\n",
		port,
	)
	if len(options.LifecycleHookCommand) > 0 {
		config.WriteString("\n[hooks.client]\nlifecycle = [")
		for index, argument := range options.LifecycleHookCommand {
			if strings.TrimSpace(argument) == "" {
				return fmt.Errorf("lifecycle hook command argument %d cannot be blank", index)
			}
			if index > 0 {
				config.WriteString(", ")
			}
			config.WriteString(strconv.Quote(argument))
		}
		config.WriteString("]\n")
	}
	config.WriteString("\n[reviewer]\nfrequency = \"off\"\n")
	if err := os.WriteFile(configPath, []byte(config.String()), 0o644); err != nil {
		return fmt.Errorf("write fixture config: %w", err)
	}
	return nil
}

func freeTCPPort(ctx context.Context) (int, error) {
	var lc net.ListenConfig
	listener, err := lc.Listen(ctx, "tcp", "127.0.0.1:0")
	if err != nil {
		return 0, fmt.Errorf("allocate fixture server port: %w", err)
	}
	defer func() { _ = listener.Close() }()
	addr, ok := listener.Addr().(*net.TCPAddr)
	if !ok {
		return 0, fmt.Errorf("fixture server listener address is %T", listener.Addr())
	}
	return addr.Port, nil
}
