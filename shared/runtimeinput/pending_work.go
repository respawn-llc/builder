package runtimeinput

import (
	"errors"
	"fmt"
	"strings"

	"core/shared/runtimeids"
)

const PendingWorkCapacity = 100

var ErrPendingWorkCapacity = errors.New("Pending Work capacity reached")
var ErrPendingWorkNotPending = errors.New("Pending Work item is not pending")

type PendingWorkRemovalError struct {
	ItemID runtimeids.QueueItemID
}

func (e *PendingWorkRemovalError) Error() string {
	if e == nil || e.ItemID.IsZero() {
		return ErrPendingWorkNotPending.Error()
	}
	return "Pending Work item " + e.ItemID.String() + " is not pending"
}

func (*PendingWorkRemovalError) Unwrap() error { return ErrPendingWorkNotPending }

type PendingWorkLane string
type PendingWorkItemKind string
type PendingWorkItemState string
type PendingWorkWorktreeTransitionKind string

const (
	PendingWorkLaneSteer PendingWorkLane = "steer"
	PendingWorkLaneQueue PendingWorkLane = "queue"

	PendingWorkItemKindMessage            PendingWorkItemKind = "message"
	PendingWorkItemKindManualCompaction   PendingWorkItemKind = "manual_compaction"
	PendingWorkItemKindWorktreeTransition PendingWorkItemKind = "worktree_transition"

	PendingWorkItemStatePending PendingWorkItemState = "pending"

	PendingWorkWorktreeTransitionEnter PendingWorkWorktreeTransitionKind = "enter"
	PendingWorkWorktreeTransitionLeave PendingWorkWorktreeTransitionKind = "leave"
)

type PendingWorkMessage struct {
	Text string `json:"text"`
}

type ManualCompactionAdmission struct {
	Guidance *string `json:"guidance,omitempty"`
}

type PendingWorkManualCompaction = ManualCompactionAdmission

type PendingWorkWorktreeTransition struct {
	Transition PendingWorkWorktreeTransitionKind `json:"transition"`
	Selector   *string                           `json:"selector,omitempty"`
}

type PendingWorkItem struct {
	ID                 runtimeids.QueueItemID         `json:"id"`
	Lane               PendingWorkLane                `json:"lane"`
	Kind               PendingWorkItemKind            `json:"kind"`
	State              PendingWorkItemState           `json:"state"`
	CanonicalInput     string                         `json:"canonical_input"`
	Message            *PendingWorkMessage            `json:"message,omitempty"`
	ManualCompaction   *PendingWorkManualCompaction   `json:"manual_compaction,omitempty"`
	WorktreeTransition *PendingWorkWorktreeTransition `json:"worktree_transition,omitempty"`
}

func (i PendingWorkItem) Validate() error {
	if i.ID.IsZero() {
		return errors.New("Pending Work item id is required")
	}
	switch i.Lane {
	case PendingWorkLaneSteer, PendingWorkLaneQueue:
	default:
		return fmt.Errorf("unknown Pending Work lane %q", i.Lane)
	}
	if i.State != PendingWorkItemStatePending {
		return fmt.Errorf("unknown Pending Work state %q", i.State)
	}
	payloads := 0
	if i.Message != nil {
		payloads++
	}
	if i.ManualCompaction != nil {
		payloads++
	}
	if i.WorktreeTransition != nil {
		payloads++
	}
	if payloads != 1 {
		return errors.New("Pending Work item requires exactly one payload")
	}

	var canonicalInput string
	switch i.Kind {
	case PendingWorkItemKindMessage:
		if i.Message == nil || strings.TrimSpace(i.Message.Text) == "" {
			return errors.New("Pending Work message text is required")
		}
		canonicalInput = i.Message.Text
	case PendingWorkItemKindManualCompaction:
		if i.Lane != PendingWorkLaneSteer {
			return errors.New("Pending Work manual compaction must use the Steer lane")
		}
		if i.ManualCompaction == nil {
			return errors.New("Pending Work manual compaction payload is required")
		}
		var err error
		canonicalInput, err = i.ManualCompaction.CanonicalInput()
		if err != nil {
			return err
		}
	case PendingWorkItemKindWorktreeTransition:
		if i.Lane != PendingWorkLaneSteer {
			return errors.New("Pending Work Worktree transition must use the Steer lane")
		}
		if i.WorktreeTransition == nil {
			return errors.New("Pending Work Worktree transition payload is required")
		}
		var err error
		canonicalInput, err = i.WorktreeTransition.CanonicalInput()
		if err != nil {
			return err
		}
	default:
		return fmt.Errorf("unknown Pending Work kind %q", i.Kind)
	}
	if i.CanonicalInput != canonicalInput {
		return errors.New("Pending Work canonical input does not match its payload")
	}
	return nil
}

func (c ManualCompactionAdmission) Validate() error {
	if c.Guidance == nil {
		return nil
	}
	if *c.Guidance == "" {
		return errors.New("Pending Work compaction guidance must not be blank")
	}
	if NormalizePendingWorkArgument(*c.Guidance) != *c.Guidance {
		return errors.New("Pending Work compaction guidance must be normalized")
	}
	return nil
}

func (c ManualCompactionAdmission) CanonicalInput() (string, error) {
	if err := c.Validate(); err != nil {
		return "", err
	}
	if c.Guidance == nil {
		return "/compact", nil
	}
	return "/compact " + *c.Guidance, nil
}

func (t PendingWorkWorktreeTransition) Validate() error {
	switch t.Transition {
	case PendingWorkWorktreeTransitionEnter:
		if t.Selector == nil || *t.Selector == "" {
			return errors.New("Pending Work Worktree enter selector is required")
		}
		if NormalizePendingWorkArgument(*t.Selector) != *t.Selector {
			return errors.New("Pending Work Worktree enter selector must be normalized")
		}
	case PendingWorkWorktreeTransitionLeave:
		if t.Selector != nil {
			return errors.New("Pending Work Worktree leave selector is forbidden")
		}
	default:
		return fmt.Errorf("unknown Pending Work Worktree transition %q", t.Transition)
	}
	return nil
}

func (t PendingWorkWorktreeTransition) CanonicalInput() (string, error) {
	if err := t.Validate(); err != nil {
		return "", err
	}
	switch t.Transition {
	case PendingWorkWorktreeTransitionEnter:
		return "/wt switch " + *t.Selector, nil
	case PendingWorkWorktreeTransitionLeave:
		return "/wt leave", nil
	default:
		panic("validated Pending Work Worktree transition is exhaustive")
	}
}

type PendingWork struct {
	Items []PendingWorkItem `json:"items"`
}

func (c PendingWork) Validate() error {
	seen := make(map[runtimeids.QueueItemID]struct{}, len(c.Items))
	steerStarted := false
	for index, item := range c.Items {
		if err := item.Validate(); err != nil {
			return fmt.Errorf("Pending Work item %d: %w", index, err)
		}
		if _, duplicate := seen[item.ID]; duplicate {
			return fmt.Errorf("Pending Work item %d repeats id %s", index, item.ID)
		}
		seen[item.ID] = struct{}{}
		if item.Lane == PendingWorkLaneSteer {
			steerStarted = true
		} else if steerStarted {
			return errors.New("Pending Work Queue items must precede Steer items")
		}
	}
	return nil
}

type PendingWorkRestoration struct {
	Kind           PendingWorkItemKind `json:"kind"`
	CanonicalInput string              `json:"canonical_input"`
}

type PendingWorkTechnicalRestoration struct {
	ItemID         runtimeids.QueueItemID `json:"item_id"`
	Kind           PendingWorkItemKind    `json:"kind"`
	CanonicalInput string                 `json:"canonical_input"`
}

func (i PendingWorkItem) Restoration() (PendingWorkRestoration, error) {
	if err := i.Validate(); err != nil {
		return PendingWorkRestoration{}, err
	}
	return PendingWorkRestoration{
		Kind:           i.Kind,
		CanonicalInput: i.CanonicalInput,
	}, nil
}

func (r PendingWorkRestoration) Validate() error {
	switch r.Kind {
	case PendingWorkItemKindMessage, PendingWorkItemKindManualCompaction, PendingWorkItemKindWorktreeTransition:
	default:
		return fmt.Errorf("Pending Work restoration kind %q is invalid", r.Kind)
	}
	if strings.TrimSpace(r.CanonicalInput) == "" {
		return errors.New("Pending Work restoration canonical input is required")
	}
	return nil
}

func (i PendingWorkItem) TechnicalRestoration() (PendingWorkTechnicalRestoration, error) {
	restoration, err := i.Restoration()
	if err != nil {
		return PendingWorkTechnicalRestoration{}, err
	}
	return PendingWorkTechnicalRestoration{
		ItemID:         i.ID,
		Kind:           restoration.Kind,
		CanonicalInput: restoration.CanonicalInput,
	}, nil
}

func (r PendingWorkTechnicalRestoration) Validate() error {
	if r.ItemID.IsZero() {
		return errors.New("Pending Work technical restoration item id is required")
	}
	return (PendingWorkRestoration{
		Kind:           r.Kind,
		CanonicalInput: r.CanonicalInput,
	}).Validate()
}

func NormalizePendingWorkArgument(value string) string {
	return strings.Join(strings.Fields(value), " ")
}
