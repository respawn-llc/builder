package workflowstore

import (
	"errors"
	"fmt"
	"hash/fnv"
	"strings"

	"core/server/workflow"
	"core/shared/runtimeids"
)

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

func LifecycleQuestionItemID(sessionID runtimeids.SessionID, promptID string) (string, error) {
	trimmedPromptID := strings.TrimSpace(promptID)
	if sessionID.IsZero() || trimmedPromptID == "" || trimmedPromptID != promptID {
		return "", errors.New("lifecycle Question session and prompt identity are required")
	}
	return "question:" + sessionID.String() + ":" + promptID, nil
}

type lifecycleQuestionKey struct {
	occurredAtUnixMs int64
	itemID           string
}

type lifecycleQuestionLocator struct {
	taskID      workflow.TaskID
	currentNode workflow.CurrentNodeReferenceKey
	scopeID     runtimeids.ExecutionScopeID
	promptID    string
}

type lifecycleQuestionIndexNode struct {
	key      lifecycleQuestionKey
	locator  lifecycleQuestionLocator
	priority uint64
	left     *lifecycleQuestionIndexNode
	right    *lifecycleQuestionIndexNode
}

func lifecycleQuestionKeyBefore(left, right lifecycleQuestionKey) bool {
	if left.occurredAtUnixMs != right.occurredAtUnixMs {
		return left.occurredAtUnixMs > right.occurredAtUnixMs
	}
	return left.itemID > right.itemID
}

func lifecycleQuestionPriority(key lifecycleQuestionKey) uint64 {
	hash := fnv.New64a()
	_, _ = hash.Write([]byte(fmt.Sprintf("%d\x00%s", key.occurredAtUnixMs, key.itemID)))
	return hash.Sum64()
}

func cloneLifecycleQuestionNode(node *lifecycleQuestionIndexNode) *lifecycleQuestionIndexNode {
	if node == nil {
		return nil
	}
	cloned := *node
	return &cloned
}

func rotateLifecycleQuestionRight(root *lifecycleQuestionIndexNode) *lifecycleQuestionIndexNode {
	next := cloneLifecycleQuestionNode(root.left)
	root = cloneLifecycleQuestionNode(root)
	root.left = next.right
	next.right = root
	return next
}

func rotateLifecycleQuestionLeft(root *lifecycleQuestionIndexNode) *lifecycleQuestionIndexNode {
	next := cloneLifecycleQuestionNode(root.right)
	root = cloneLifecycleQuestionNode(root)
	root.right = next.left
	next.left = root
	return next
}

func insertLifecycleQuestion(
	root *lifecycleQuestionIndexNode,
	key lifecycleQuestionKey,
	locator lifecycleQuestionLocator,
) (*lifecycleQuestionIndexNode, error) {
	if root == nil {
		return &lifecycleQuestionIndexNode{
			key:      key,
			locator:  locator,
			priority: lifecycleQuestionPriority(key),
		}, nil
	}
	if key == root.key {
		return nil, fmt.Errorf("lifecycle Question index contains duplicate item %q", key.itemID)
	}
	next := cloneLifecycleQuestionNode(root)
	var err error
	if lifecycleQuestionKeyBefore(key, root.key) {
		next.left, err = insertLifecycleQuestion(root.left, key, locator)
		if err == nil && next.left.priority < next.priority {
			next = rotateLifecycleQuestionRight(next)
		}
	} else {
		next.right, err = insertLifecycleQuestion(root.right, key, locator)
		if err == nil && next.right.priority < next.priority {
			next = rotateLifecycleQuestionLeft(next)
		}
	}
	return next, err
}

func mergeLifecycleQuestionIndexes(left, right *lifecycleQuestionIndexNode) *lifecycleQuestionIndexNode {
	switch {
	case left == nil:
		return right
	case right == nil:
		return left
	case left.priority < right.priority:
		next := cloneLifecycleQuestionNode(left)
		next.right = mergeLifecycleQuestionIndexes(left.right, right)
		return next
	default:
		next := cloneLifecycleQuestionNode(right)
		next.left = mergeLifecycleQuestionIndexes(left, right.left)
		return next
	}
}

func removeLifecycleQuestion(
	root *lifecycleQuestionIndexNode,
	key lifecycleQuestionKey,
) (*lifecycleQuestionIndexNode, error) {
	if root == nil {
		return nil, fmt.Errorf("lifecycle Question index item %q is absent", key.itemID)
	}
	if key == root.key {
		return mergeLifecycleQuestionIndexes(root.left, root.right), nil
	}
	next := cloneLifecycleQuestionNode(root)
	var err error
	if lifecycleQuestionKeyBefore(key, root.key) {
		next.left, err = removeLifecycleQuestion(root.left, key)
	} else {
		next.right, err = removeLifecycleQuestion(root.right, key)
	}
	return next, err
}

func lifecycleQuestionFacts(
	taskID workflow.TaskID,
	entry lifecycleTaskEntry,
) ([]struct {
	key     lifecycleQuestionKey
	locator lifecycleQuestionLocator
}, error) {
	facts := make([]struct {
		key     lifecycleQuestionKey
		locator lifecycleQuestionLocator
	}, 0)
	for currentNodeKey, exact := range entry.exact {
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
			facts = append(facts, struct {
				key     lifecycleQuestionKey
				locator lifecycleQuestionLocator
			}{
				key: lifecycleQuestionKey{occurredAtUnixMs: occurredAtUnixMs, itemID: itemID},
				locator: lifecycleQuestionLocator{
					taskID:      taskID,
					currentNode: currentNodeKey,
					scopeID:     exact.ScopeID,
					promptID:    prompt.ID,
				},
			})
		}
	}
	return facts, nil
}

func updateLifecycleQuestionIndex(
	index *lifecycleQuestionIndexNode,
	taskID workflow.TaskID,
	before lifecycleTaskEntry,
	after lifecycleTaskEntry,
) (*lifecycleQuestionIndexNode, error) {
	beforeFacts, err := lifecycleQuestionFacts(taskID, before)
	if err != nil {
		return nil, err
	}
	afterFacts, err := lifecycleQuestionFacts(taskID, after)
	if err != nil {
		return nil, err
	}
	next := index
	for _, fact := range beforeFacts {
		next, err = removeLifecycleQuestion(next, fact.key)
		if err != nil {
			return nil, err
		}
	}
	for _, fact := range afterFacts {
		next, err = insertLifecycleQuestion(next, fact.key, fact.locator)
		if err != nil {
			return nil, err
		}
	}
	return next, nil
}

func lifecycleQuestionAfterCursor(key lifecycleQuestionKey, cursor LifecycleQuestionCursor) bool {
	if !cursor.HasValue {
		return true
	}
	return key.occurredAtUnixMs < cursor.OccurredAtUnixMs ||
		(key.occurredAtUnixMs == cursor.OccurredAtUnixMs && key.itemID < cursor.ItemID)
}

func lifecycleQuestionPage(
	index *lifecycleQuestionIndexNode,
	root lifecycleRoot,
	cursor LifecycleQuestionCursor,
	limit int,
) ([]LifecyclePendingQuestion, error) {
	if limit <= 0 {
		return nil, errors.New("lifecycle Question page limit must be positive")
	}
	if cursor.HasValue && (cursor.OccurredAtUnixMs <= 0 || strings.TrimSpace(cursor.ItemID) == "") {
		return nil, errors.New("lifecycle Question cursor is invalid")
	}
	if cursor.HasValue && strings.TrimSpace(cursor.ItemID) != cursor.ItemID {
		return nil, errors.New("lifecycle Question cursor item id is invalid")
	}
	out := make([]LifecyclePendingQuestion, 0, limit)
	var visit func(*lifecycleQuestionIndexNode) error
	visit = func(node *lifecycleQuestionIndexNode) error {
		if node == nil || len(out) == limit {
			return nil
		}
		if err := visit(node.left); err != nil {
			return err
		}
		if len(out) == limit {
			return nil
		}
		if lifecycleQuestionAfterCursor(node.key, cursor) {
			entry, exists := root[node.locator.taskID]
			if !exists {
				return fmt.Errorf("lifecycle Question index Task %q is absent", node.locator.taskID)
			}
			exact, exists := entry.exact[node.locator.currentNode]
			if !exists || exact.ScopeID != node.locator.scopeID || exact.Agent == nil {
				return fmt.Errorf("lifecycle Question index Exact scope %s is absent", node.locator.scopeID)
			}
			var prompt *LifecyclePendingPrompt
			for index := range exact.PendingPrompts {
				if exact.PendingPrompts[index].ID == node.locator.promptID {
					prompt = &exact.PendingPrompts[index]
					break
				}
			}
			if prompt == nil || prompt.CreatedAt.UnixMilli() != node.key.occurredAtUnixMs {
				return fmt.Errorf("lifecycle Question index prompt %q is inconsistent", node.locator.promptID)
			}
			expectedItemID, err := LifecycleQuestionItemID(exact.Agent.SessionID, prompt.ID)
			if err != nil {
				return err
			}
			if expectedItemID != node.key.itemID {
				return fmt.Errorf("lifecycle Question index item %q does not match %q", node.key.itemID, expectedItemID)
			}
			out = append(out, LifecyclePendingQuestion{
				TaskID:      node.locator.taskID,
				CurrentNode: exact.CurrentNode,
				SessionID:   exact.Agent.SessionID,
				Prompt:      cloneLifecyclePendingPrompt(*prompt),
			})
		}
		return visit(node.right)
	}
	if err := visit(index); err != nil {
		return nil, err
	}
	return out, nil
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
