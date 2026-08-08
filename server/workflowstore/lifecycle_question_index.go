package workflowstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"core/server/metadata"
	"core/server/metadata/sqlitelifecyclegen"
	"core/server/workflow"
	"core/shared/runtimeids"
)

const lifecycleQuestionIndexFilePattern = ".lifecycle-question-index-*.sqlite*"

var lifecycleQuestionIndexFiles = struct {
	sync.Mutex
	activeByRoot map[string]map[string]struct{}
}{
	activeByRoot: make(map[string]map[string]struct{}),
}

type LifecycleQuestionCursor struct {
	OccurredAtUnixMs int64
	ItemID           string
	HasValue         bool
}

type LifecyclePendingQuestion struct {
	TaskID      workflow.TaskID
	CurrentNode workflow.CurrentNodeReference
	SessionID   runtimeids.SessionID
	Prompt      LifecyclePendingPrompt
}

type lifecycleQuestionFact struct {
	occurredAtUnixMs int64
	itemID           string
	currentNode      workflow.CurrentNodeReference
	scopeID          runtimeids.ExecutionScopeID
	prompt           LifecyclePendingPromptReference
}

type lifecycleQuestionFactIdentity struct {
	occurredAtUnixMs int64
	itemID           string
}

type lifecycleQuestionPayloadKey struct {
	scopeID  runtimeids.ExecutionScopeID
	promptID string
}

type lifecycleQuestionIndex struct {
	db      *sql.DB
	queries *sqlitelifecyclegen.Queries
	root    string
	path    string
}

type lifecycleQuestionReadSnapshot struct {
	tx      *sql.Tx
	queries *sqlitelifecyclegen.Queries
}

func LifecycleQuestionItemID(sessionID runtimeids.SessionID, promptID string) (string, error) {
	trimmedPromptID := strings.TrimSpace(promptID)
	if sessionID.IsZero() || trimmedPromptID == "" || trimmedPromptID != promptID {
		return "", errors.New("lifecycle Question session and prompt identity are required")
	}
	return "question:" + sessionID.String() + ":" + promptID, nil
}

func openLifecycleQuestionIndex(ctx context.Context, persistenceRoot string) (*lifecycleQuestionIndex, error) {
	persistenceRoot = strings.TrimSpace(persistenceRoot)
	if persistenceRoot == "" {
		return nil, errors.New("lifecycle Question index persistence root is required")
	}
	file, err := createLifecycleQuestionIndexFile(persistenceRoot)
	if err != nil {
		return nil, fmt.Errorf("create lifecycle Question index: %w", err)
	}
	path := file.Name()
	if err := file.Close(); err != nil {
		return nil, errors.Join(
			fmt.Errorf("close lifecycle Question index seed: %w", err),
			unregisterLifecycleQuestionIndexFile(persistenceRoot, path),
			removeLifecycleQuestionIndexFiles(path),
		)
	}
	dsnURL := url.URL{Scheme: "file", Path: path}
	query := url.Values{}
	query.Add("_pragma", "journal_mode(WAL)")
	query.Add("_pragma", "synchronous(NORMAL)")
	query.Add("_pragma", "busy_timeout(5000)")
	dsnURL.RawQuery = query.Encode()
	db, err := sql.Open("sqlite", dsnURL.String())
	if err != nil {
		return nil, errors.Join(
			fmt.Errorf("open lifecycle Question index: %w", err),
			unregisterLifecycleQuestionIndexFile(persistenceRoot, path),
			removeLifecycleQuestionIndexFiles(path),
		)
	}
	db.SetMaxOpenConns(4)
	db.SetMaxIdleConns(4)
	queries := sqlitelifecyclegen.New(db)
	if err := queries.CreateLifecycleQuestionIndex(ctx); err != nil {
		return nil, errors.Join(
			fmt.Errorf("create lifecycle Question index schema: %w", err),
			db.Close(),
			unregisterLifecycleQuestionIndexFile(persistenceRoot, path),
			removeLifecycleQuestionIndexFiles(path),
		)
	}
	if err := queries.ClearLifecycleQuestionIndex(ctx); err != nil {
		return nil, errors.Join(
			fmt.Errorf("clear lifecycle Question index: %w", err),
			db.Close(),
			unregisterLifecycleQuestionIndexFile(persistenceRoot, path),
			removeLifecycleQuestionIndexFiles(path),
		)
	}
	return &lifecycleQuestionIndex{
		db:      db,
		queries: queries,
		root:    persistenceRoot,
		path:    path,
	}, nil
}

func createLifecycleQuestionIndexFile(persistenceRoot string) (*os.File, error) {
	lifecycleQuestionIndexFiles.Lock()
	defer lifecycleQuestionIndexFiles.Unlock()
	paths, err := filepath.Glob(filepath.Join(persistenceRoot, lifecycleQuestionIndexFilePattern))
	if err != nil {
		return nil, fmt.Errorf("list stale lifecycle Question indexes: %w", err)
	}
	active := lifecycleQuestionIndexFiles.activeByRoot[persistenceRoot]
	var cleanupErr error
	for _, path := range paths {
		basePath := strings.TrimSuffix(strings.TrimSuffix(path, "-shm"), "-wal")
		if _, live := active[basePath]; live {
			continue
		}
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			cleanupErr = errors.Join(cleanupErr, fmt.Errorf("remove stale lifecycle Question index %q: %w", path, err))
		}
	}
	if cleanupErr != nil {
		return nil, cleanupErr
	}
	file, err := os.CreateTemp(persistenceRoot, ".lifecycle-question-index-*.sqlite")
	if err != nil {
		return nil, fmt.Errorf("create lifecycle Question index: %w", err)
	}
	if active == nil {
		active = make(map[string]struct{})
		lifecycleQuestionIndexFiles.activeByRoot[persistenceRoot] = active
	}
	active[file.Name()] = struct{}{}
	return file, nil
}

func unregisterLifecycleQuestionIndexFile(persistenceRoot string, path string) error {
	lifecycleQuestionIndexFiles.Lock()
	defer lifecycleQuestionIndexFiles.Unlock()
	active := lifecycleQuestionIndexFiles.activeByRoot[persistenceRoot]
	delete(active, path)
	if len(active) == 0 {
		delete(lifecycleQuestionIndexFiles.activeByRoot, persistenceRoot)
	}
	return nil
}

func removeLifecycleQuestionIndexFiles(path string) error {
	var cleanupErr error
	for _, candidate := range []string{path, path + "-shm", path + "-wal"} {
		if err := os.Remove(candidate); err != nil && !errors.Is(err, os.ErrNotExist) {
			cleanupErr = errors.Join(cleanupErr, fmt.Errorf("remove lifecycle Question index %q: %w", candidate, err))
		}
	}
	return cleanupErr
}

func (i *lifecycleQuestionIndex) close() error {
	if i == nil {
		return nil
	}
	var clearErr error
	if i.queries != nil {
		clearErr = i.queries.ClearLifecycleQuestionIndex(context.Background())
	}
	var closeErr error
	if i.db != nil {
		closeErr = i.db.Close()
	}
	removeErr := removeLifecycleQuestionIndexFiles(i.path)
	unregisterErr := unregisterLifecycleQuestionIndexFile(i.root, i.path)
	i.db = nil
	i.queries = nil
	i.root = ""
	i.path = ""
	return errors.Join(clearErr, closeErr, removeErr, unregisterErr)
}

func (i *lifecycleQuestionIndex) beginRead(ctx context.Context) (*lifecycleQuestionReadSnapshot, error) {
	if i == nil || i.db == nil {
		return nil, errors.New("lifecycle Question index is required")
	}
	tx, err := i.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, fmt.Errorf("begin lifecycle Question index read: %w", err)
	}
	queries := sqlitelifecyclegen.New(tx)
	if _, err := queries.AnchorLifecycleQuestionIndex(ctx); err != nil {
		return nil, errors.Join(
			fmt.Errorf("anchor lifecycle Question index read: %w", err),
			tx.Rollback(),
		)
	}
	return &lifecycleQuestionReadSnapshot{tx: tx, queries: queries}, nil
}

func (s *lifecycleQuestionReadSnapshot) close() error {
	if s == nil || s.tx == nil {
		return nil
	}
	err := s.tx.Rollback()
	if errors.Is(err, sql.ErrTxDone) {
		err = nil
	}
	s.tx = nil
	s.queries = nil
	return err
}

func lifecycleQuestionFacts(taskID workflow.TaskID, entry lifecycleTaskEntry) ([]lifecycleQuestionFact, error) {
	facts := make([]lifecycleQuestionFact, 0)
	for _, exact := range entry.exact {
		if exact.CurrentNode.TaskID != taskID {
			return nil, fmt.Errorf(
				"lifecycle Question Exact Current Node belongs to Task %q, want %q",
				exact.CurrentNode.TaskID,
				taskID,
			)
		}
		if exact.Agent == nil {
			continue
		}
		for _, prompt := range exact.PendingPrompts {
			occurredAtUnixMs := prompt.CreatedAt.UnixMilli()
			if occurredAtUnixMs <= 0 {
				return nil, fmt.Errorf("lifecycle Question %q has invalid occurrence time", prompt.ID)
			}
			itemID, err := LifecycleQuestionItemID(exact.Agent.SessionID, prompt.ID)
			if err != nil {
				return nil, err
			}
			facts = append(facts, lifecycleQuestionFact{
				occurredAtUnixMs: occurredAtUnixMs,
				itemID:           itemID,
				currentNode:      exact.CurrentNode,
				scopeID:          exact.ScopeID,
				prompt:           prompt,
			})
		}
	}
	return facts, nil
}

func lifecycleQuestionFactKey(fact lifecycleQuestionFact) lifecycleQuestionFactIdentity {
	return lifecycleQuestionFactIdentity{
		occurredAtUnixMs: fact.occurredAtUnixMs,
		itemID:           fact.itemID,
	}
}

func lifecycleQuestionFactEqual(left, right lifecycleQuestionFact) bool {
	if left.occurredAtUnixMs != right.occurredAtUnixMs ||
		left.itemID != right.itemID ||
		!left.currentNode.Equal(right.currentNode) ||
		left.scopeID != right.scopeID ||
		left.prompt != right.prompt {
		return false
	}
	return true
}

func lifecycleQuestionFactsByKey(
	facts []lifecycleQuestionFact,
) (map[lifecycleQuestionFactIdentity]lifecycleQuestionFact, error) {
	byKey := make(map[lifecycleQuestionFactIdentity]lifecycleQuestionFact, len(facts))
	for _, fact := range facts {
		key := lifecycleQuestionFactKey(fact)
		if _, duplicate := byKey[key]; duplicate {
			return nil, fmt.Errorf("lifecycle Question index contains duplicate item %q", fact.itemID)
		}
		byKey[key] = fact
	}
	return byKey, nil
}

func validateLifecycleQuestionFactsUnchanged(
	taskID workflow.TaskID,
	before lifecycleTaskEntry,
	after lifecycleTaskEntry,
) error {
	beforeFacts, err := lifecycleQuestionFacts(taskID, before)
	if err != nil {
		return err
	}
	afterFacts, err := lifecycleQuestionFacts(taskID, after)
	if err != nil {
		return err
	}
	beforeByKey, err := lifecycleQuestionFactsByKey(beforeFacts)
	if err != nil {
		return err
	}
	afterByKey, err := lifecycleQuestionFactsByKey(afterFacts)
	if err != nil {
		return err
	}
	if len(beforeByKey) != len(afterByKey) {
		return errors.New("lifecycle pending Questions may change only through Exact prompt publication")
	}
	for key, beforeFact := range beforeByKey {
		afterFact, exists := afterByKey[key]
		if !exists || !lifecycleQuestionFactEqual(beforeFact, afterFact) {
			return errors.New("lifecycle pending Questions may change only through Exact prompt publication")
		}
	}
	return nil
}

func (i *lifecycleQuestionIndex) replaceTaskQuestions(
	ctx context.Context,
	taskID workflow.TaskID,
	before lifecycleTaskEntry,
	after lifecycleTaskEntry,
	insertedPayloads map[lifecycleQuestionPayloadKey]LifecyclePendingPrompt,
) (err error) {
	beforeFacts, err := lifecycleQuestionFacts(taskID, before)
	if err != nil {
		return err
	}
	afterFacts, err := lifecycleQuestionFacts(taskID, after)
	if err != nil {
		return err
	}
	beforeByKey, err := lifecycleQuestionFactsByKey(beforeFacts)
	if err != nil {
		return err
	}
	afterByKey, err := lifecycleQuestionFactsByKey(afterFacts)
	if err != nil {
		return err
	}
	changed := false
	for key, beforeFact := range beforeByKey {
		afterFact, exists := afterByKey[key]
		if !exists || !lifecycleQuestionFactEqual(beforeFact, afterFact) {
			changed = true
			break
		}
	}
	if !changed {
		for key, afterFact := range afterByKey {
			beforeFact, exists := beforeByKey[key]
			if !exists || !lifecycleQuestionFactEqual(beforeFact, afterFact) {
				changed = true
				break
			}
		}
	}
	if !changed {
		if len(insertedPayloads) != 0 {
			return errors.New("published lifecycle Question payload did not add an index item")
		}
		return nil
	}
	tx, err := i.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin lifecycle Question index mutation: %w", err)
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, tx.Rollback())
		}
	}()
	queries := sqlitelifecyclegen.New(tx)
	usedPayloads := make(map[lifecycleQuestionPayloadKey]struct{}, len(insertedPayloads))
	for key, beforeFact := range beforeByKey {
		afterFact, exists := afterByKey[key]
		if exists && lifecycleQuestionFactEqual(beforeFact, afterFact) {
			continue
		}
		deleted, deleteErr := queries.DeleteLifecycleQuestion(ctx, lifecycleQuestionDeleteParams(beforeFact))
		if deleteErr != nil {
			return fmt.Errorf("delete lifecycle Question index item %q: %w", beforeFact.itemID, deleteErr)
		}
		if deleted != 1 {
			return fmt.Errorf("lifecycle Question index item %q delete count = %d, want 1", beforeFact.itemID, deleted)
		}
	}
	for key, afterFact := range afterByKey {
		beforeFact, exists := beforeByKey[key]
		if exists && lifecycleQuestionFactEqual(beforeFact, afterFact) {
			continue
		}
		payloadKey := lifecycleQuestionPayloadKey{
			scopeID:  afterFact.scopeID,
			promptID: afterFact.prompt.ID,
		}
		payload, exists := insertedPayloads[payloadKey]
		if !exists {
			return fmt.Errorf("lifecycle Question index item %q has no published payload", afterFact.itemID)
		}
		params, paramsErr := lifecycleQuestionInsertParams(afterFact, payload)
		if paramsErr != nil {
			return paramsErr
		}
		if insertErr := queries.InsertLifecycleQuestion(ctx, params); insertErr != nil {
			return fmt.Errorf("insert lifecycle Question index item %q: %w", afterFact.itemID, insertErr)
		}
		usedPayloads[payloadKey] = struct{}{}
	}
	if len(usedPayloads) != len(insertedPayloads) {
		return errors.New("published lifecycle Question payload does not match an inserted index item")
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit lifecycle Question index mutation: %w", err)
	}
	return nil
}

func lifecycleQuestionInsertParams(
	fact lifecycleQuestionFact,
	prompt LifecyclePendingPrompt,
) (sqlitelifecyclegen.InsertLifecycleQuestionParams, error) {
	if lifecyclePendingPromptReference(prompt) != fact.prompt {
		return sqlitelifecyclegen.InsertLifecycleQuestionParams{}, errors.New("lifecycle Question payload identity does not match its lifecycle fact")
	}
	if err := validateLifecyclePendingPrompt(prompt); err != nil {
		return sqlitelifecyclegen.InsertLifecycleQuestionParams{}, err
	}
	suggestionsJSON, err := json.Marshal(append([]string{}, prompt.Suggestions...))
	if err != nil {
		return sqlitelifecyclegen.InsertLifecycleQuestionParams{}, fmt.Errorf("encode lifecycle Question suggestions: %w", err)
	}
	approvalDecisionsJSON, err := json.Marshal(append([]LifecycleApprovalDecision{}, prompt.ApprovalDecisions...))
	if err != nil {
		return sqlitelifecyclegen.InsertLifecycleQuestionParams{}, fmt.Errorf("encode lifecycle Question approval decisions: %w", err)
	}
	params := sqlitelifecyclegen.InsertLifecycleQuestionParams{
		OccurredAtUnixMs:      fact.occurredAtUnixMs,
		ItemID:                fact.itemID,
		TaskID:                string(fact.currentNode.TaskID),
		NodeID:                string(fact.currentNode.NodeID),
		ScopeID:               fact.scopeID.String(),
		PromptID:              fact.prompt.ID,
		PromptKind:            int64(fact.prompt.Kind),
		Question:              prompt.Question,
		SuggestionsJSON:       string(suggestionsJSON),
		ApprovalDecisionsJSON: string(approvalDecisionsJSON),
	}
	if branchKey, present := fact.currentNode.TransitionBranchKey(); present {
		params.TransitionBranchKey = sql.NullString{
			String: string(branchKey),
			Valid:  true,
		}
	}
	if prompt.RecommendedOptionIndex != nil {
		params.RecommendedOptionIndex = sql.NullInt64{
			Int64: int64(*prompt.RecommendedOptionIndex),
			Valid: true,
		}
	}
	return params, nil
}

func lifecycleQuestionDeleteParams(fact lifecycleQuestionFact) sqlitelifecyclegen.DeleteLifecycleQuestionParams {
	params := sqlitelifecyclegen.DeleteLifecycleQuestionParams{
		OccurredAtUnixMs: fact.occurredAtUnixMs,
		ItemID:           fact.itemID,
		TaskID:           string(fact.currentNode.TaskID),
		NodeID:           string(fact.currentNode.NodeID),
		ScopeID:          fact.scopeID.String(),
		PromptID:         fact.prompt.ID,
	}
	if branchKey, present := fact.currentNode.TransitionBranchKey(); present {
		params.TransitionBranchKey = sql.NullString{
			String: string(branchKey),
			Valid:  true,
		}
	}
	return params
}

func lifecycleQuestionPage(
	ctx context.Context,
	snapshot *lifecycleQuestionReadSnapshot,
	root lifecycleRoot,
	cursor LifecycleQuestionCursor,
	limit int,
) ([]LifecyclePendingQuestion, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if snapshot == nil || snapshot.queries == nil {
		return nil, errors.New("lifecycle Question read snapshot is required")
	}
	if limit <= 0 {
		return nil, errors.New("lifecycle Question page limit must be positive")
	}
	if cursor.HasValue &&
		(cursor.OccurredAtUnixMs <= 0 ||
			strings.TrimSpace(cursor.ItemID) == "" ||
			strings.TrimSpace(cursor.ItemID) != cursor.ItemID) {
		return nil, errors.New("lifecycle Question cursor is invalid")
	}
	rows, err := snapshot.queries.ListLifecycleQuestions(ctx, sqlitelifecyclegen.ListLifecycleQuestionsParams{
		CursorActive:           metadata.SQLiteBoolInt64(cursor.HasValue),
		CursorOccurredAtUnixMs: cursor.OccurredAtUnixMs,
		CursorItemID:           cursor.ItemID,
		LimitRows:              int64(limit),
	})
	if err != nil {
		return nil, fmt.Errorf("list lifecycle Questions: %w", err)
	}
	return lifecycleQuestionsFromRows(root, rows)
}

func lifecycleQuestionsForTask(
	ctx context.Context,
	snapshot *lifecycleQuestionReadSnapshot,
	root lifecycleRoot,
	taskID workflow.TaskID,
) ([]LifecyclePendingQuestion, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if snapshot == nil || snapshot.queries == nil {
		return nil, errors.New("lifecycle Question read snapshot is required")
	}
	if taskID == "" {
		return nil, errors.New("lifecycle Question Task id is required")
	}
	rows, err := snapshot.queries.ListLifecycleQuestionsForTask(ctx, string(taskID))
	if err != nil {
		return nil, fmt.Errorf("list lifecycle Questions for Task %q: %w", taskID, err)
	}
	return lifecycleQuestionsFromRows(root, rows)
}

func lifecycleQuestionsFromRows(
	root lifecycleRoot,
	rows []sqlitelifecyclegen.LifecycleQuestionRecord,
) ([]LifecyclePendingQuestion, error) {
	out := make([]LifecyclePendingQuestion, 0, len(rows))
	for _, row := range rows {
		taskID := workflow.TaskID(row.TaskID)
		var branchKey *workflow.TransitionBranchKey
		if row.TransitionBranchKey.Valid {
			value := workflow.TransitionBranchKey(row.TransitionBranchKey.String)
			branchKey = &value
		}
		reference, err := workflow.NewCurrentNodeReference(taskID, workflow.NodeID(row.NodeID), branchKey)
		if err != nil {
			return nil, fmt.Errorf("decode lifecycle Question Current Node: %w", err)
		}
		key, err := reference.Key()
		if err != nil {
			return nil, err
		}
		entry, exists := root[taskID]
		if !exists {
			return nil, fmt.Errorf("lifecycle Question index Task %q is absent", taskID)
		}
		exact, exists := entry.exact[key]
		if !exists || exact.ScopeID.String() != row.ScopeID || exact.Agent == nil {
			return nil, fmt.Errorf("lifecycle Question index Exact scope %q is absent", row.ScopeID)
		}
		var promptReference *LifecyclePendingPromptReference
		for index := range exact.PendingPrompts {
			if exact.PendingPrompts[index].ID == row.PromptID {
				promptReference = &exact.PendingPrompts[index]
				break
			}
		}
		if promptReference == nil ||
			promptReference.CreatedAt.UnixMilli() != row.OccurredAtUnixMs ||
			int64(promptReference.Kind) != row.PromptKind {
			return nil, fmt.Errorf("lifecycle Question index prompt %q is inconsistent", row.PromptID)
		}
		prompt, err := lifecyclePendingPromptFromRow(row)
		if err != nil {
			return nil, err
		}
		expectedItemID, err := LifecycleQuestionItemID(exact.Agent.SessionID, prompt.ID)
		if err != nil {
			return nil, err
		}
		if expectedItemID != row.ItemID {
			return nil, fmt.Errorf("lifecycle Question index item %q does not match %q", row.ItemID, expectedItemID)
		}
		out = append(out, LifecyclePendingQuestion{
			TaskID:      taskID,
			CurrentNode: exact.CurrentNode,
			SessionID:   exact.Agent.SessionID,
			Prompt:      prompt,
		})
	}
	return out, nil
}

func lifecyclePendingPromptFromRow(
	row sqlitelifecyclegen.LifecycleQuestionRecord,
) (LifecyclePendingPrompt, error) {
	kind := LifecyclePendingPromptKind(row.PromptKind)
	var suggestions []string
	if err := json.Unmarshal([]byte(row.SuggestionsJSON), &suggestions); err != nil {
		return LifecyclePendingPrompt{}, fmt.Errorf("decode lifecycle Question %q suggestions: %w", row.PromptID, err)
	}
	var approvalDecisions []LifecycleApprovalDecision
	if err := json.Unmarshal([]byte(row.ApprovalDecisionsJSON), &approvalDecisions); err != nil {
		return LifecyclePendingPrompt{}, fmt.Errorf("decode lifecycle Question %q approval decisions: %w", row.PromptID, err)
	}
	prompt := LifecyclePendingPrompt{
		ID:                row.PromptID,
		Kind:              kind,
		CreatedAt:         time.UnixMilli(row.OccurredAtUnixMs).UTC(),
		Question:          row.Question,
		Suggestions:       suggestions,
		ApprovalDecisions: approvalDecisions,
	}
	if row.RecommendedOptionIndex.Valid {
		value := int(row.RecommendedOptionIndex.Int64)
		if int64(value) != row.RecommendedOptionIndex.Int64 {
			return LifecyclePendingPrompt{}, fmt.Errorf("lifecycle Question %q recommended option index overflows int", row.PromptID)
		}
		prompt.RecommendedOptionIndex = &value
	}
	if err := validateLifecyclePendingPrompt(prompt); err != nil {
		return LifecyclePendingPrompt{}, fmt.Errorf("decode lifecycle Question %q: %w", row.PromptID, err)
	}
	return prompt, nil
}

func cloneLifecyclePendingPrompt(prompt LifecyclePendingPrompt) LifecyclePendingPrompt {
	cloned := prompt
	cloned.Suggestions = append([]string(nil), prompt.Suggestions...)
	cloned.ApprovalDecisions = append([]LifecycleApprovalDecision(nil), prompt.ApprovalDecisions...)
	if prompt.RecommendedOptionIndex != nil {
		value := *prompt.RecommendedOptionIndex
		cloned.RecommendedOptionIndex = &value
	}
	return cloned
}
