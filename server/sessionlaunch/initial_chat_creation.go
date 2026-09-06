package sessionlaunch

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"core/server/auth"
	"core/server/launch"
	"core/server/llm"
	"core/server/session"
	"core/shared/config"
	chatpb "core/shared/protoapi/gen/kent/api/chat"
	chatsettingspb "core/shared/protoapi/gen/kent/api/chat_settings"
	"core/shared/serverapi"
	"core/shared/textutil"
)

type InitialChatCreation struct {
	Settings   InitialChatSettings
	InputDraft *string
}

type InitialChatSettings struct {
	AgentRole             string
	Supervisor            serverapi.ChatSettingsSupervisorValue
	Thinking              *string
	Fast                  *bool
	QuestionsEnabled      bool
	AutoCompactionEnabled bool
}

func initialChatCreationFromGenerated(
	settings *chatpb.InitialChatSettings,
	inputDraft *string,
) (*InitialChatCreation, error) {
	if settings == nil {
		if inputDraft != nil {
			return nil, errors.New("initial Chat input draft requires settings")
		}
		return nil, nil
	}
	var supervisor serverapi.ChatSettingsSupervisorValue
	switch settings.Supervisor {
	case chatsettingspb.SupervisorValue_SUPERVISOR_VALUE_OFF:
		supervisor = serverapi.ChatSettingsSupervisorOff
	case chatsettingspb.SupervisorValue_SUPERVISOR_VALUE_AFTER_EDITS:
		supervisor = serverapi.ChatSettingsSupervisorAfterEdits
	case chatsettingspb.SupervisorValue_SUPERVISOR_VALUE_ALWAYS:
		supervisor = serverapi.ChatSettingsSupervisorAlways
	default:
		return nil, fmt.Errorf("generated initial Chat Supervisor %v is invalid", settings.Supervisor)
	}
	if settings.QuestionsEnabled == nil {
		return nil, errors.New("generated initial Chat Questions selection is required")
	}
	if settings.AutoCompactionEnabled == nil {
		return nil, errors.New("generated initial Chat Auto-compaction selection is required")
	}
	creation := &InitialChatCreation{
		Settings: InitialChatSettings{
			AgentRole:             settings.AgentRole,
			Supervisor:            supervisor,
			Thinking:              textutil.Pointer(settings.Thinking),
			Fast:                  textutil.Pointer(settings.Fast),
			QuestionsEnabled:      *settings.QuestionsEnabled,
			AutoCompactionEnabled: *settings.AutoCompactionEnabled,
		},
		InputDraft: textutil.Pointer(inputDraft),
	}
	return creation, nil
}

func (c InitialChatCreation) Validate(mode launch.Mode, intent serverapi.SessionLaunchIntent) error {
	if err := launch.ValidateInitialChatCreationTarget(mode, intent); err != nil {
		return err
	}
	return c.Settings.Validate()
}

func (s InitialChatSettings) Validate() error {
	agent := strings.TrimSpace(s.AgentRole)
	if agent == "" {
		return errors.New("initial Chat Agent is required")
	}
	if agent != s.AgentRole {
		return errors.New("initial Chat Agent must not have leading or trailing whitespace")
	}
	switch s.Supervisor {
	case serverapi.ChatSettingsSupervisorOff,
		serverapi.ChatSettingsSupervisorAfterEdits,
		serverapi.ChatSettingsSupervisorAlways:
	default:
		return fmt.Errorf("initial Chat Supervisor %q is invalid", s.Supervisor)
	}
	if s.Thinking != nil {
		thinking := strings.TrimSpace(*s.Thinking)
		if thinking == "" {
			return errors.New("initial Chat Thinking is required when present")
		}
		if thinking != *s.Thinking {
			return errors.New("initial Chat Thinking must not have leading or trailing whitespace")
		}
	}
	return nil
}

func (s *Service) prepareInitialChatCreation(
	ctx context.Context,
	app config.App,
	creation InitialChatCreation,
) (*session.ChatDraftState, error) {
	authState := auth.EmptyState()
	if s.authStates != nil {
		var err error
		authState, err = s.authStates.StoredState(ctx)
		if err != nil {
			return nil, err
		}
	}
	catalog, err := launch.PrepareChatAgentCatalog(app, authState, false)
	if err != nil {
		return nil, err
	}
	entry, available := catalog.Lookup(creation.Settings.AgentRole)
	if !available {
		entry, available = catalog.Lookup(config.DefaultSubagentRole)
		if !available {
			return nil, errors.New("default Chat Agent baseline is missing")
		}
	}
	settings := entry.Settings.Baseline
	if available && entry.Choice.Role == creation.Settings.AgentRole {
		settings.Supervisor = string(creation.Settings.Supervisor)
		if creation.Settings.Thinking != nil {
			thinking := *creation.Settings.Thinking
			_, enumerated := llm.LookupModelCapabilityContract(entry.ResolvedSettings.Model)
			if len(entry.Settings.SupportedThinkingValues) > 0 &&
				(!enumerated || slices.Contains(entry.Settings.SupportedThinkingValues, thinking)) {
				settings.Thinking = thinking
			}
		}
		if creation.Settings.Fast != nil && entry.Settings.FastAvailable {
			settings.Fast = *creation.Settings.Fast
		}
		settings.Questions = creation.Settings.QuestionsEnabled
		settings.AutoCompaction = creation.Settings.AutoCompactionEnabled
	}
	state, err := session.ChatSettingsStateFromCompleteSettings(entry.Choice.Role, settings)
	if err != nil {
		return nil, err
	}
	draft := ""
	if creation.InputDraft != nil {
		draft = *creation.InputDraft
	}
	return &session.ChatDraftState{
		Message: draft,
		Agent:   state.Agent,
		Settings: &session.ChatSettingsOverrides{
			Supervisor:     textutil.Pointer(state.Settings.Supervisor),
			Thinking:       textutil.Pointer(state.Settings.Thinking),
			Fast:           textutil.Pointer(state.Settings.Fast),
			Questions:      textutil.Pointer(state.Settings.Questions),
			AutoCompaction: textutil.Pointer(state.Settings.AutoCompaction),
		},
	}, nil
}
