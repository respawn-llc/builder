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

const (
	PendingWorkLaneSteer PendingWorkLane = "steer"
	PendingWorkLaneQueue PendingWorkLane = "queue"

	PendingWorkItemKindMessage          PendingWorkItemKind = "message"
	PendingWorkItemKindManualCompaction PendingWorkItemKind = "manual_compaction"

	PendingWorkItemStatePending PendingWorkItemState = "pending"
)

type PendingWorkMessage struct {
	Text string `json:"text"`
}

type ManualCompactionAdmission struct {
	Guidance         *string `json:"guidance,omitempty"`
	RestorationInput string  `json:"restoration_input"`
}

type PendingWorkManualCompaction = ManualCompactionAdmission

type PendingWorkItem struct {
	ID               runtimeids.QueueItemID       `json:"id"`
	Lane             PendingWorkLane              `json:"lane"`
	Kind             PendingWorkItemKind          `json:"kind"`
	State            PendingWorkItemState         `json:"state"`
	Message          *PendingWorkMessage          `json:"message,omitempty"`
	ManualCompaction *PendingWorkManualCompaction `json:"manual_compaction,omitempty"`
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
	if payloads != 1 {
		return errors.New("Pending Work item requires exactly one payload")
	}
	switch i.Kind {
	case PendingWorkItemKindMessage:
		if i.Message == nil || strings.TrimSpace(i.Message.Text) == "" {
			return errors.New("Pending Work message text is required")
		}
	case PendingWorkItemKindManualCompaction:
		if i.Lane != PendingWorkLaneSteer {
			return errors.New("Pending Work manual compaction must use the Steer lane")
		}
		if i.ManualCompaction == nil {
			return errors.New("Pending Work manual compaction payload is required")
		}
		if err := i.ManualCompaction.Validate(); err != nil {
			return err
		}
	default:
		return fmt.Errorf("unknown Pending Work kind %q", i.Kind)
	}
	return nil
}

func (c ManualCompactionAdmission) Validate() error {
	if c.Guidance != nil && strings.TrimSpace(*c.Guidance) == "" {
		return errors.New("Pending Work compaction guidance must not be blank")
	}
	if strings.TrimSpace(c.RestorationInput) == "" {
		return errors.New("Pending Work compaction restoration input is required")
	}
	return nil
}

type PendingWork struct {
	Items []PendingWorkItem `json:"items"`
}

func (c PendingWork) Validate() error {
	seen := make(map[runtimeids.QueueItemID]struct{}, len(c.Items))
	queueStarted := false
	for index, item := range c.Items {
		if err := item.Validate(); err != nil {
			return fmt.Errorf("Pending Work item %d: %w", index, err)
		}
		if _, duplicate := seen[item.ID]; duplicate {
			return fmt.Errorf("Pending Work item %d repeats id %s", index, item.ID)
		}
		seen[item.ID] = struct{}{}
		if item.Lane == PendingWorkLaneQueue {
			queueStarted = true
		} else if queueStarted {
			return errors.New("Pending Work Steer items must precede Queue items")
		}
	}
	return nil
}

type PendingWorkManualCompactionRestoration struct {
	Input string `json:"input"`
}

type PendingWorkRestoration struct {
	Kind             PendingWorkItemKind                     `json:"kind"`
	Message          *PendingWorkMessage                     `json:"message,omitempty"`
	ManualCompaction *PendingWorkManualCompactionRestoration `json:"manual_compaction,omitempty"`
}

func (i PendingWorkItem) Restoration() (PendingWorkRestoration, error) {
	switch i.Kind {
	case PendingWorkItemKindMessage:
		if i.Message == nil {
			return PendingWorkRestoration{}, errors.New("Pending Work message restoration payload is required")
		}
		return PendingWorkRestoration{Kind: PendingWorkItemKindMessage, Message: &PendingWorkMessage{Text: i.Message.Text}}, nil
	case PendingWorkItemKindManualCompaction:
		if i.ManualCompaction == nil {
			return PendingWorkRestoration{}, errors.New("Pending Work compaction restoration payload is required")
		}
		return PendingWorkRestoration{
			Kind: PendingWorkItemKindManualCompaction,
			ManualCompaction: &PendingWorkManualCompactionRestoration{
				Input: i.ManualCompaction.RestorationInput,
			},
		}, nil
	default:
		return PendingWorkRestoration{}, fmt.Errorf("Pending Work restoration kind %q is invalid", i.Kind)
	}
}

func (r PendingWorkRestoration) Validate() error {
	payloads := 0
	if r.Message != nil {
		payloads++
	}
	if r.ManualCompaction != nil {
		payloads++
	}
	if payloads != 1 {
		return errors.New("Pending Work restoration requires exactly one payload")
	}
	switch r.Kind {
	case PendingWorkItemKindMessage:
		if r.Message == nil || strings.TrimSpace(r.Message.Text) == "" {
			return errors.New("Pending Work message restoration text is required")
		}
	case PendingWorkItemKindManualCompaction:
		if r.ManualCompaction == nil || strings.TrimSpace(r.ManualCompaction.Input) == "" {
			return errors.New("Pending Work compaction restoration input is required")
		}
	default:
		return fmt.Errorf("Pending Work restoration kind %q is invalid", r.Kind)
	}
	return nil
}
