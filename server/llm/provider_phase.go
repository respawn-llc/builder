package llm

// ProviderPhase is the authoritative phase-presence fact for one provider
// response. A nil *ProviderPhase means the provider adapter did not supply the
// fact; a non-nil value can represent either structural phase absence or an
// explicit supported phase.
type ProviderPhase struct {
	value *MessagePhase
}

func AbsentProviderPhase() *ProviderPhase {
	return &ProviderPhase{}
}

func CommentaryProviderPhase() *ProviderPhase {
	phase := MessagePhaseCommentary
	return &ProviderPhase{value: &phase}
}

func FinalProviderPhase() *ProviderPhase {
	phase := MessagePhaseFinal
	return &ProviderPhase{value: &phase}
}

func (p *ProviderPhase) Value() *MessagePhase {
	if p == nil || p.value == nil {
		return nil
	}
	value := *p.value
	return &value
}

func (p *ProviderPhase) Is(phase MessagePhase) bool {
	value := p.Value()
	return value != nil && *value == phase
}

func (p *ProviderPhase) IsAbsent() bool {
	return p != nil && p.value == nil
}

func providerPhaseProjection(phase *ProviderPhase) MessagePhase {
	value := phase.Value()
	if value == nil {
		return ""
	}
	return *value
}
