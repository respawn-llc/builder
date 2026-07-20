package app

import "core/shared/config"

func clientSettingsForInteractiveServer(server interactiveSessionServer) config.ClientSettings {
	configured, ok := server.(interface {
		ClientSettings() config.ClientSettings
	})
	if !ok {
		return config.ClientSettings{}
	}
	return configured.ClientSettings()
}
