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
	"core/shared/config"
)

func PrepareConfigAndBinding(ctx context.Context, persistenceRoot string, workspaceRoot string) error {
	return PrepareConfigAndBindingWithOptions(ctx, persistenceRoot, workspaceRoot, ConfigOptions{})
}

type ConfigOptions struct {
	ServerPort                       *int
	LifecycleHookCommand             []string
	ModelContextWindow               *int
	ContextCompactionThresholdTokens *int
	PreSubmitCompactionLeadTokens    *int
	CompactionMode                   *config.CompactionMode
	ProviderCapabilities             *config.ProviderCapabilitiesOverride
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

func WriteConfigWithOptions(
	ctx context.Context,
	persistenceRoot string,
	options ConfigOptions,
) error {
	if err := os.MkdirAll(persistenceRoot, 0o755); err != nil {
		return fmt.Errorf("create persistence root: %w", err)
	}
	var port int
	if options.ServerPort != nil {
		port = *options.ServerPort
		if port <= 0 {
			return fmt.Errorf("fixture server port must be positive")
		}
	} else {
		resolved, err := freeTCPPort(ctx)
		if err != nil {
			return err
		}
		port = resolved
	}
	configPath := filepath.Join(persistenceRoot, "config.toml")
	var config strings.Builder
	fmt.Fprintf(
		&config,
		"model = \"gpt-5\"\nprovider_override = \"openai\"\nopenai_base_url = \"http://127.0.0.1:1/v1\"\nserver_port = %d\ntheme = \"dark\"\n",
		port,
	)
	if options.ModelContextWindow != nil {
		fmt.Fprintf(&config, "model_context_window = %d\n", *options.ModelContextWindow)
	}
	if options.ContextCompactionThresholdTokens != nil {
		fmt.Fprintf(&config, "context_compaction_threshold_tokens = %d\n", *options.ContextCompactionThresholdTokens)
	}
	if options.PreSubmitCompactionLeadTokens != nil {
		fmt.Fprintf(&config, "pre_submit_compaction_lead_tokens = %d\n", *options.PreSubmitCompactionLeadTokens)
	}
	if options.CompactionMode != nil {
		fmt.Fprintf(&config, "compaction_mode = %s\n", strconv.Quote(string(*options.CompactionMode)))
	}
	if options.ProviderCapabilities != nil {
		capabilities := *options.ProviderCapabilities
		if strings.TrimSpace(capabilities.ProviderID) == "" {
			return fmt.Errorf("fixture provider capabilities require provider_id")
		}
		config.WriteString("\n[provider_capabilities]\n")
		fmt.Fprintf(&config, "provider_id = %s\n", strconv.Quote(capabilities.ProviderID))
		fmt.Fprintf(&config, "supports_responses_api = %t\n", capabilities.SupportsResponsesAPI)
		fmt.Fprintf(&config, "supports_responses_compact = %t\n", capabilities.SupportsResponsesCompact)
		fmt.Fprintf(&config, "supports_request_input_token_count = %t\n", capabilities.SupportsRequestInputTokenCount)
		fmt.Fprintf(&config, "is_openai_first_party = %t\n", capabilities.IsOpenAIFirstParty)
	}
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
