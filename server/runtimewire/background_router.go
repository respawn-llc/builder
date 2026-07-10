package runtimewire

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"core/server/runtime"
	shelltool "core/server/tools/shell"
	"core/shared/valuecopy"

	"github.com/google/uuid"
)

type BackgroundEventRouter struct {
	mu          sync.RWMutex
	active      map[string]activeRuntime
	outputLimit int
	outputMode  shelltool.BackgroundOutputMode
}

type activeRuntime struct {
	engine      *runtime.Engine
	activatedAt time.Time
}

func NewBackgroundEventRouter(background *shelltool.Manager, outputLimit int, outputMode shelltool.BackgroundOutputMode) *BackgroundEventRouter {
	router := &BackgroundEventRouter{active: make(map[string]activeRuntime), outputLimit: outputLimit, outputMode: outputMode}
	if background != nil {
		background.SetEventHandler(router.handle)
	}
	return router
}

func (r *BackgroundEventRouter) SetActiveSession(sessionID string, engine *runtime.Engine) {
	trimmedSessionID := strings.TrimSpace(sessionID)
	if trimmedSessionID == "" || engine == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.active == nil {
		r.active = make(map[string]activeRuntime)
	}
	r.active[trimmedSessionID] = activeRuntime{engine: engine, activatedAt: time.Now().UTC()}
}

func (r *BackgroundEventRouter) ClearActiveSession(sessionID string, engine *runtime.Engine) {
	trimmedSessionID := strings.TrimSpace(sessionID)
	if trimmedSessionID == "" || engine == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.active) == 0 {
		return
	}
	active, ok := r.active[trimmedSessionID]
	if !ok || active.engine != engine {
		return
	}
	delete(r.active, trimmedSessionID)
}

func (r *BackgroundEventRouter) handle(evt shelltool.Event) {
	ownerSessionID := strings.TrimSpace(evt.Snapshot.OwnerSessionID)
	if ownerSessionID == "" {
		return
	}
	if evt.Snapshot.ActivityID == uuid.Nil || evt.Snapshot.ActivityID.Version() != 4 {
		panic(fmt.Sprintf("background event missing UUIDv4 activity id: process_id=%q activity_id=%q", evt.Snapshot.ID, evt.Snapshot.ActivityID))
	}
	r.mu.RLock()
	activeRuntime, ok := r.active[ownerSessionID]
	outputLimit := r.outputLimit
	outputMode := r.outputMode
	r.mu.RUnlock()
	if !ok || activeRuntime.engine == nil {
		return
	}
	summary := shelltool.BackgroundNoticeSummary{}
	if evt.Type == shelltool.EventCompleted || evt.Type == shelltool.EventKilled {
		summary = shelltool.SummarizeBackgroundEvent(evt, shelltool.BackgroundNoticeOptions{
			MaxChars:          outputLimit,
			SuccessOutputMode: outputMode,
		})
	}
	shouldNotify := !evt.NoticeSuppressed
	if shouldNotify && !evt.Snapshot.FinishedAt.IsZero() && evt.Snapshot.FinishedAt.Before(activeRuntime.activatedAt) {
		shouldNotify = false
	}
	eventType := runtime.BackgroundShellEventBackgrounded
	switch evt.Type {
	case shelltool.EventCompleted:
		eventType = runtime.BackgroundShellEventCompleted
	case shelltool.EventKilled:
		eventType = runtime.BackgroundShellEventKilled
	}
	activeRuntime.engine.HandleBackgroundShellUpdate(runtime.BackgroundShellEvent{
		Type:              eventType,
		ID:                evt.Snapshot.ID,
		ActivityID:        evt.Snapshot.ActivityID,
		State:             evt.Snapshot.State,
		Command:           evt.Snapshot.Command,
		Workdir:           evt.Snapshot.Workdir,
		LogPath:           evt.Snapshot.LogPath,
		NoticeText:        summary.DetailText,
		CompactText:       summary.CondensedText,
		Preview:           evt.Preview,
		PreviewRemoved:    evt.Removed,
		ExitCode:          valuecopy.Pointer(evt.Snapshot.ExitCode),
		UserRequestedKill: evt.Snapshot.KillRequested,
		NoticeSuppressed:  evt.NoticeSuppressed,
	}, shouldNotify)
}

func (r *BackgroundEventRouter) Handle(evt shelltool.Event) {
	r.handle(evt)
}
