package appfixture

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"

	"core/server/metadata"
)

func PrepareConfigAndBinding(ctx context.Context, persistenceRoot string, workspaceRoot string) error {
	if err := os.MkdirAll(persistenceRoot, 0o755); err != nil {
		return fmt.Errorf("create persistence root: %w", err)
	}
	port, err := freeTCPPort()
	if err != nil {
		return err
	}
	configPath := filepath.Join(persistenceRoot, "config.toml")
	config := fmt.Sprintf("model = \"gpt-5\"\nprovider_override = \"openai\"\nopenai_base_url = \"http://127.0.0.1:1/v1\"\nserver_port = %d\n\n[reviewer]\nfrequency = \"off\"\n", port)
	if err := os.WriteFile(configPath, []byte(config), 0o644); err != nil {
		return fmt.Errorf("write fixture config: %w", err)
	}
	if _, err := metadata.RegisterBinding(ctx, persistenceRoot, workspaceRoot); err != nil {
		return fmt.Errorf("register fixture workspace binding: %w", err)
	}
	return nil
}

func freeTCPPort() (int, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
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
