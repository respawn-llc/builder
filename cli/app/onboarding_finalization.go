package app

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"core/shared/client"
	"core/shared/serverapi"
	"core/shared/theme"

	tea "github.com/charmbracelet/bubbletea"
)

const onboardingFinalizationTimeout = 30 * time.Second

type onboardingFinalization struct {
	finalizer client.OnboardingFinalizeClient
	flowCtx   context.Context
	timeout   time.Duration

	mu      sync.Mutex
	attempt *onboardingFinalizationAttempt
}

type onboardingFinalizationAttempt struct {
	outcome onboardingFinalizeDoneMsg
	done    chan struct{}
}

func newOnboardingFinalization(finalizer client.OnboardingFinalizeClient, flowCtx context.Context) *onboardingFinalization {
	if flowCtx == nil {
		flowCtx = context.Background()
	}
	return &onboardingFinalization{
		finalizer: finalizer,
		flowCtx:   flowCtx,
		timeout:   onboardingFinalizationTimeout,
	}
}

func (f *onboardingFinalization) submit(request serverapi.OnboardingFinalizeRequest, writeDefaults bool, selectedTheme string) tea.Cmd {
	return func() tea.Msg {
		if err := f.start(request, writeDefaults, selectedTheme); err != nil {
			return onboardingFinalizeDoneMsg{err: err}
		}
		return f.wait()
	}
}

func (f *onboardingFinalization) start(request serverapi.OnboardingFinalizeRequest, writeDefaults bool, selectedTheme string) error {
	if f == nil || f.finalizer == nil {
		return errors.New("onboarding finalization client is required")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.flowCtx.Err(); err != nil {
		return err
	}
	if f.attempt != nil {
		return errors.New("onboarding finalization has already been submitted")
	}
	attempt := &onboardingFinalizationAttempt{done: make(chan struct{})}
	f.attempt = attempt
	go f.run(attempt, request, writeDefaults, selectedTheme)
	return nil
}

func (f *onboardingFinalization) run(attempt *onboardingFinalizationAttempt, request serverapi.OnboardingFinalizeRequest, writeDefaults bool, selectedTheme string) {
	timeout := f.timeout
	if timeout <= 0 {
		timeout = onboardingFinalizationTimeout
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(f.flowCtx), timeout)
	defer cancel()

	response, err := f.finalizer.FinalizeOnboarding(ctx, request)
	outcome := onboardingFinalizeDoneMsg{submitted: true}
	switch {
	case err == nil && !response.Completed:
		outcome.err = errors.New("onboarding finalization did not report completion")
	case err == nil:
		outcome.result = onboardingResult{
			Completed:            true,
			CreatedDefaultConfig: writeDefaults,
			SettingsPath:         response.SettingsPath,
			EffectiveTheme:       theme.Resolve(selectedTheme),
		}
	case errors.Is(err, context.DeadlineExceeded):
		outcome.err = fmt.Errorf("onboarding finalization outcome is indeterminate after %s: %w", timeout, err)
	case onboardingFinalizationIndeterminate(err):
		outcome.err = fmt.Errorf("onboarding finalization outcome is indeterminate: %w", err)
	default:
		outcome.err = err
	}

	attempt.outcome = outcome
	close(attempt.done)
}

func (f *onboardingFinalization) wait() onboardingFinalizeDoneMsg {
	f.mu.Lock()
	attempt := f.attempt
	f.mu.Unlock()
	if attempt == nil {
		return onboardingFinalizeDoneMsg{err: errors.New("onboarding finalization was not submitted")}
	}
	<-attempt.done
	return attempt.outcome
}

func (f *onboardingFinalization) waitIfSubmitted() (onboardingFinalizeDoneMsg, bool) {
	if f == nil {
		return onboardingFinalizeDoneMsg{}, false
	}
	f.mu.Lock()
	attempt := f.attempt
	f.mu.Unlock()
	if attempt == nil {
		return onboardingFinalizeDoneMsg{}, false
	}
	<-attempt.done
	return attempt.outcome, true
}

func (f *onboardingFinalization) releaseRecoverableAttempt() error {
	if f == nil {
		return errors.New("onboarding finalization is required")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.attempt == nil {
		return errors.New("recoverable onboarding finalization attempt is required")
	}
	select {
	case <-f.attempt.done:
		f.attempt = nil
		return nil
	default:
		return errors.New("cannot release an in-flight onboarding finalization attempt")
	}
}
