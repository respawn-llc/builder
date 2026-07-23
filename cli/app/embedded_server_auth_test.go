package app

import "core/server/auth"

func (s *embeddedAppServer) AuthManager() *auth.Manager {
	if s == nil || s.inner == nil {
		return nil
	}
	return s.inner.AuthManager()
}

func (s *embeddedAppServer) OAuthOptions() auth.OpenAIOAuthOptions {
	if s == nil || s.inner == nil {
		return auth.OpenAIOAuthOptions{}
	}
	return s.inner.OAuthOptions()
}
