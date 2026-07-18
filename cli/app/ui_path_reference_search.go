package app

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/sahilm/fuzzy"
)

var errPathReferenceWorkspaceUnavailable = errors.New("workspace root unavailable for path autocomplete")

type uiPathReferenceSearch interface {
	Events() <-chan uiPathReferenceSearchEvent
	StartPrewarm(workspaceRoot string)
	Search(req uiPathReferenceSearchRequest)
	Stop()
}

type uiPathReferenceCommandRunner interface {
	Output(ctx context.Context, dir string, name string, args ...string) ([]byte, error)
}

type uiPathReferenceMatcher interface {
	Match(query string, candidates []uiPathReferenceCandidate, limit int) []uiPathReferenceCandidate
}

type execUIPathReferenceCommandRunner struct{}

func (execUIPathReferenceCommandRunner) Output(ctx context.Context, dir string, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	cmd.Env = sanitizedGitEnv(os.Environ())
	return cmd.Output()
}

type fuzzyUIPathReferenceMatcher struct{}

type uiPathReferenceCandidateSource []uiPathReferenceCandidate

func (s uiPathReferenceCandidateSource) String(i int) string {
	return s[i].Path
}

func (s uiPathReferenceCandidateSource) Len() int {
	return len(s)
}

func (fuzzyUIPathReferenceMatcher) Match(query string, candidates []uiPathReferenceCandidate, limit int) []uiPathReferenceCandidate {
	if strings.TrimSpace(query) == "" || len(candidates) == 0 || limit <= 0 {
		return nil
	}
	matches := fuzzy.FindFrom(query, uiPathReferenceCandidateSource(candidates))
	if len(matches) == 0 {
		return nil
	}
	sort.SliceStable(matches, func(i, j int) bool {
		if matches[i].Score != matches[j].Score {
			return matches[i].Score > matches[j].Score
		}
		left := candidates[matches[i].Index].Path
		right := candidates[matches[j].Index].Path
		if len([]rune(left)) != len([]rune(right)) {
			return len([]rune(left)) < len([]rune(right))
		}
		return left < right
	})
	results := make([]uiPathReferenceCandidate, 0, min(limit, len(matches)))
	for _, match := range matches {
		results = append(results, candidates[match.Index])
		if len(results) == limit {
			break
		}
	}
	return results
}

type uiPathReferenceCorpusSnapshot struct {
	Candidates []uiPathReferenceCandidate
}

type uiPathReferenceSearchService struct {
	events       chan uiPathReferenceSearchEvent
	requests     chan uiPathReferenceSearchRequestMessage
	runner       uiPathReferenceCommandRunner
	matcher      uiPathReferenceMatcher
	buildTimeout time.Duration
	loadingDelay time.Duration
	stop         chan struct{}
	stopped      chan struct{}
	stopOnce     sync.Once
}

type uiPathReferenceBuildDone struct {
	workspaceRoot string
	generation    uint64
	snapshot      uiPathReferenceCorpusSnapshot
	err           error
}

type uiPathReferencePendingSearch struct {
	req        uiPathReferenceSearchRequest
	generation uint64
}

type uiPathReferenceMatchDone struct {
	search  uiPathReferencePendingSearch
	matches []uiPathReferenceCandidate
}

type uiPathReferenceLoadingElapsed struct {
	search uiPathReferencePendingSearch
}

type uiPathReferenceSearchState struct {
	workspaceRoot          string
	readyGeneration        uint64
	pendingBuildGeneration uint64
	snapshot               *uiPathReferenceCorpusSnapshot
	building               bool
	failed                 bool
	pendingSearch          *uiPathReferencePendingSearch
	runningSearch          *uiPathReferencePendingSearch
}

func newUIPathReferenceSearch() uiPathReferenceSearch {
	service := &uiPathReferenceSearchService{
		events:       make(chan uiPathReferenceSearchEvent, 64),
		requests:     make(chan uiPathReferenceSearchRequestMessage, 64),
		runner:       execUIPathReferenceCommandRunner{},
		matcher:      fuzzyUIPathReferenceMatcher{},
		buildTimeout: uiPathReferenceBuildTimeout,
		loadingDelay: uiPathReferenceLoadingDelay,
		stop:         make(chan struct{}),
		stopped:      make(chan struct{}),
	}
	go service.run()
	return service
}

func (s *uiPathReferenceSearchService) Events() <-chan uiPathReferenceSearchEvent {
	if s == nil {
		return nil
	}
	return s.events
}

func (s *uiPathReferenceSearchService) StartPrewarm(workspaceRoot string) {
	if s == nil {
		return
	}
	select {
	case <-s.stop:
		return
	default:
	}
	s.requests <- uiPathReferencePrewarmRequest{workspaceRoot: strings.TrimSpace(workspaceRoot)}
}

func (s *uiPathReferenceSearchService) Search(req uiPathReferenceSearchRequest) {
	if s == nil || strings.TrimSpace(req.NormalizedQuery) == "" {
		return
	}
	req.WorkspaceRoot = strings.TrimSpace(req.WorkspaceRoot)
	select {
	case <-s.stop:
		return
	default:
	}
	s.requests <- req
}

func (s *uiPathReferenceSearchService) Stop() {
	if s == nil {
		return
	}
	s.stopOnce.Do(func() {
		close(s.stop)
		<-s.stopped
	})
}

func (s *uiPathReferenceSearchService) run() {
	defer close(s.stopped)
	buildDone := make(chan uiPathReferenceBuildDone, 8)
	matchDone := make(chan uiPathReferenceMatchDone, 8)
	loadingDone := make(chan uiPathReferenceLoadingElapsed, 16)
	state := uiPathReferenceSearchState{}
	for {
		select {
		case <-s.stop:
			return
		case raw := <-s.requests:
			if raw == nil {
				continue
			}
			switch msg := raw.(type) {
			case uiPathReferencePrewarmRequest:
				state.ensureWorkspace(msg.workspaceRoot)
				s.ensureCorpus(&state, buildDone)
			case uiPathReferenceSearchRequest:
				state.ensureWorkspace(msg.WorkspaceRoot)
				generation := s.ensureCorpus(&state, buildDone)
				search := uiPathReferencePendingSearch{req: msg, generation: generation}
				state.pendingSearch = &search
				go s.armLoadingDelay(search, loadingDone)
				s.startPendingSearch(&state, matchDone)
			}
		case msg := <-buildDone:
			if msg.workspaceRoot != state.workspaceRoot || msg.generation != state.pendingBuildGeneration {
				continue
			}
			state.building = false
			state.pendingBuildGeneration = 0
			if msg.err != nil {
				state.failed = true
				state.snapshot = nil
				s.events <- uiPathReferenceCorpusFailedMsg{WorkspaceRoot: msg.workspaceRoot, CorpusGeneration: msg.generation, Err: msg.err}
				continue
			}
			state.failed = false
			state.readyGeneration = msg.generation
			snapshot := msg.snapshot
			state.snapshot = &snapshot
			s.events <- uiPathReferenceCorpusReadyMsg{WorkspaceRoot: msg.workspaceRoot, CorpusGeneration: msg.generation}
			s.startPendingSearch(&state, matchDone)
		case msg := <-matchDone:
			if !matchesPendingSearch(state.runningSearch, &msg.search) {
				continue
			}
			state.runningSearch = nil
			s.events <- uiPathReferenceMatchResultMsg{
				WorkspaceRoot:    msg.search.req.WorkspaceRoot,
				CorpusGeneration: msg.search.generation,
				DraftToken:       msg.search.req.DraftToken,
				QueryToken:       msg.search.req.QueryToken,
				NormalizedQuery:  msg.search.req.NormalizedQuery,
				Matches:          msg.matches,
			}
			if matchesPendingSearch(state.pendingSearch, &msg.search) {
				state.pendingSearch = nil
			}
			s.startPendingSearch(&state, matchDone)
		case msg := <-loadingDone:
			if matchesPendingSearch(state.pendingSearch, &msg.search) || matchesPendingSearch(state.runningSearch, &msg.search) {
				s.events <- uiPathReferenceLoadingDelayMsg{
					WorkspaceRoot:    msg.search.req.WorkspaceRoot,
					CorpusGeneration: msg.search.generation,
					DraftToken:       msg.search.req.DraftToken,
					QueryToken:       msg.search.req.QueryToken,
					NormalizedQuery:  msg.search.req.NormalizedQuery,
				}
			}
		}
	}
}

func (s *uiPathReferenceSearchService) armLoadingDelay(search uiPathReferencePendingSearch, done chan<- uiPathReferenceLoadingElapsed) {
	if s.loadingDelay <= 0 {
		return
	}
	timer := time.NewTimer(s.loadingDelay)
	defer timer.Stop()
	select {
	case <-s.stop:
		return
	case <-timer.C:
	}
	select {
	case <-s.stop:
		return
	case done <- uiPathReferenceLoadingElapsed{search: search}:
	}
}

func (s *uiPathReferenceSearchService) ensureCorpus(state *uiPathReferenceSearchState, buildDone chan<- uiPathReferenceBuildDone) uint64 {
	if state.snapshot != nil {
		return state.readyGeneration
	}
	if state.building {
		return state.pendingBuildGeneration
	}
	state.building = true
	state.pendingBuildGeneration = nextNonZeroToken(max(state.readyGeneration, state.pendingBuildGeneration))
	workspaceRoot := state.workspaceRoot
	generation := state.pendingBuildGeneration
	go s.buildCorpus(workspaceRoot, generation, buildDone)
	return generation
}

func (s *uiPathReferenceSearchService) buildCorpus(workspaceRoot string, generation uint64, done chan<- uiPathReferenceBuildDone) {
	if workspaceRoot == "" {
		done <- uiPathReferenceBuildDone{workspaceRoot: workspaceRoot, generation: generation, err: errPathReferenceWorkspaceUnavailable}
		return
	}
	ctx := context.Background()
	var cancel context.CancelFunc
	if s.buildTimeout > 0 {
		ctx, cancel = context.WithTimeout(ctx, s.buildTimeout)
		defer cancel()
	}
	snapshot, err := s.loadCorpusSnapshot(ctx, workspaceRoot)
	select {
	case <-s.stop:
		return
	case done <- uiPathReferenceBuildDone{workspaceRoot: workspaceRoot, generation: generation, snapshot: snapshot, err: err}:
	}
}

func (s *uiPathReferenceSearchService) loadCorpusSnapshot(ctx context.Context, workspaceRoot string) (uiPathReferenceCorpusSnapshot, error) {
	output, err := s.runner.Output(ctx, workspaceRoot, "rg", "--no-config", "--files", "-0", "--hidden", "-g", "!.git")
	if err != nil {
		if isEmptyRipgrepFilesResult(err, output) {
			return uiPathReferenceCorpusSnapshot{}, nil
		}
		return uiPathReferenceCorpusSnapshot{}, err
	}
	lines := bytes.Split(output, []byte{0})
	fileSeen := make(map[string]struct{}, len(lines))
	dirSeen := make(map[string]struct{}, len(lines))
	candidates := make([]uiPathReferenceCandidate, 0, len(lines))
	for _, raw := range lines {
		candidate := normalizePathReferenceCandidate(string(raw))
		if candidate == "" {
			continue
		}
		if _, exists := fileSeen[candidate]; exists {
			continue
		}
		fileSeen[candidate] = struct{}{}
		candidates = append(candidates, uiPathReferenceCandidate{Path: candidate})
		for _, dir := range derivePathReferenceDirectories(candidate) {
			if _, exists := dirSeen[dir]; exists {
				continue
			}
			dirSeen[dir] = struct{}{}
			candidates = append(candidates, uiPathReferenceCandidate{Path: dir, Directory: true})
		}
	}
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].Path < candidates[j].Path
	})
	return uiPathReferenceCorpusSnapshot{Candidates: terminalSafePathReferenceCandidates(candidates)}, nil
}

func isEmptyRipgrepFilesResult(err error, output []byte) bool {
	if err == nil || strings.TrimSpace(string(output)) != "" {
		return false
	}
	type exitCoder interface {
		ExitCode() int
	}
	var coded exitCoder
	if errors.As(err, &coded) {
		return coded.ExitCode() == 1
	}
	return false
}

func normalizePathReferenceCandidate(raw string) string {
	if raw == "" {
		return ""
	}
	normalized := filepath.ToSlash(raw)
	normalized = strings.TrimPrefix(normalized, "./")
	normalized = path.Clean(normalized)
	if normalized == "." || normalized == "" || normalized == ".git" || strings.HasPrefix(normalized, ".git/") {
		return ""
	}
	return normalized
}

func derivePathReferenceDirectories(filePath string) []string {
	seen := make(map[string]struct{})
	dirs := make([]string, 0, 8)
	for dir := path.Dir(filePath); dir != "." && dir != "/" && dir != ""; dir = path.Dir(dir) {
		if _, exists := seen[dir]; exists {
			break
		}
		seen[dir] = struct{}{}
		dirs = append(dirs, dir)
	}
	return dirs
}

func (s *uiPathReferenceSearchService) startPendingSearch(state *uiPathReferenceSearchState, done chan<- uiPathReferenceMatchDone) {
	if state.snapshot == nil || state.runningSearch != nil || state.pendingSearch == nil {
		return
	}
	search := *state.pendingSearch
	state.runningSearch = &search
	go func(snapshot uiPathReferenceCorpusSnapshot, pending uiPathReferencePendingSearch) {
		matches := s.matcher.Match(pending.req.NormalizedQuery, snapshot.Candidates, slashCommandPickerLines)
		select {
		case <-s.stop:
			return
		case done <- uiPathReferenceMatchDone{search: pending, matches: matches}:
		}
	}(*state.snapshot, search)
}

func (s *uiPathReferenceSearchState) ensureWorkspace(workspaceRoot string) {
	if s.workspaceRoot == workspaceRoot {
		return
	}
	s.workspaceRoot = workspaceRoot
	s.readyGeneration = 0
	s.pendingBuildGeneration = 0
	s.snapshot = nil
	s.building = false
	s.failed = false
	s.pendingSearch = nil
	s.runningSearch = nil
}

func matchesPendingSearch(left, right *uiPathReferencePendingSearch) bool {
	if left == nil || right == nil {
		return false
	}
	return left.generation == right.generation &&
		left.req.WorkspaceRoot == right.req.WorkspaceRoot &&
		left.req.DraftToken == right.req.DraftToken &&
		left.req.QueryToken == right.req.QueryToken &&
		left.req.NormalizedQuery == right.req.NormalizedQuery
}

func waitPathReferenceSearchEvent(ch <-chan uiPathReferenceSearchEvent) tea.Cmd {
	if ch == nil {
		return nil
	}
	return func() tea.Msg {
		msg, ok := <-ch
		if !ok {
			return nil
		}
		return msg
	}
}

func loadPathReferenceCorpusSnapshotForWorkspace(ctx context.Context, workspaceRoot string) (uiPathReferenceCorpusSnapshot, error) {
	if _, err := exec.LookPath("rg"); err != nil {
		return uiPathReferenceCorpusSnapshot{}, err
	}
	if info, err := os.Stat(workspaceRoot); err != nil || !info.IsDir() {
		if err == nil {
			err = errors.New("workspace root is not a directory")
		}
		return uiPathReferenceCorpusSnapshot{}, err
	}
	service := &uiPathReferenceSearchService{runner: execUIPathReferenceCommandRunner{}}
	return service.loadCorpusSnapshot(ctx, workspaceRoot)
}
