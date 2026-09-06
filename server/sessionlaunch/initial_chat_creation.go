package sessionlaunch

import (
	"context"
	"errors"
	"slices"

	"core/server/auth"
	"core/server/launch"
	"core/server/llm"
	"core/server/session"
	"core/shared/config"
	"core/shared/protoapi"
	chatsettingspb "core/shared/protoapi/gen/kent/api/chat_settings"
	"core/shared/serverapi"
	"core/shared/textutil"
)

type InitialChatCreation struct {
	Settings   serverapi.InitialChatSettings
	InputDraft *string
}

func initialChatCreationFromGenerated(
	settings *chatsettingspb.InitialChatSettings,
	inputDraft *string,
) (*InitialChatCreation, error) {
	if settings == nil {
		if inputDraft != nil {
			return nil, errors.New("initial Chat input draft requires settings")
		}
		return nil, nil
	}
	selection, err := protoapi.InitialChatSettingsFromProto(settings)
	if err != nil {
		return nil, err
	}
	creation := &InitialChatCreation{
		Settings:   selection,
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
