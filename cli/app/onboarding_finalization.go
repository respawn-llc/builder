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

	mu        sync.Mutex
	submitted bool
	outcome   onboardingFinalizeDoneMsg
	done      chan struct{}
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
	if err := f.flowCtx.Err(); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.submitted {
		return errors.New("onboarding finalization has already been submitted")
	}
	f.submitted = true
	f.done = make(chan struct{})
	go f.run(request, writeDefaults, selectedTheme)
	return nil
}

func (f *onboardingFinalization) run(request serverapi.OnboardingFinalizeRequest, writeDefaults bool, selectedTheme string) {
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

	f.mu.Lock()
	f.outcome = outcome
	close(f.done)
	f.mu.Unlock()
}

func (f *onboardingFinalization) wait() onboardingFinalizeDoneMsg {
	f.mu.Lock()
	done := f.done
	f.mu.Unlock()
	if done == nil {
		return onboardingFinalizeDoneMsg{err: errors.New("onboarding finalization was not submitted")}
	}
	<-done
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.outcome
}

func (f *onboardingFinalization) waitIfSubmitted() (onboardingFinalizeDoneMsg, bool) {
	if f == nil {
		return onboardingFinalizeDoneMsg{}, false
	}
	f.mu.Lock()
	submitted := f.submitted
	f.mu.Unlock()
	if !submitted {
		return onboardingFinalizeDoneMsg{}, false
	}
	return f.wait(), true
}
