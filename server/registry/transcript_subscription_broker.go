package registry

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"strings"
	"sync"

	"core/shared/clientui"
	"core/shared/serverapi"

	"github.com/google/uuid"
)

type transcriptSubscriptionBroker struct {
	mu          sync.Mutex
	nextID      uint64
	closed      bool
	subscribers map[uint64]*transcriptSubscription
}

type transcriptSubscription struct {
	ch      chan clientui.TranscriptSubscriptionMessage
	onClose func()

	mu       sync.Mutex
	nextSeq  uint64
	err      error
	done     bool
	contract transcriptSubscriptionContract
}

var transcriptContractViolationsPanic bool

func newTranscriptSubscriptionBroker() *transcriptSubscriptionBroker {
	return &transcriptSubscriptionBroker{subscribers: make(map[uint64]*transcriptSubscription)}
}

func (b *transcriptSubscriptionBroker) SubscriberCount() int {
	if b == nil {
		return 0
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.subscribers)
}

func (b *transcriptSubscriptionBroker) Subscribe(hydration clientui.TranscriptSubscriptionMessage) (*transcriptSubscription, error) {
	if b == nil {
		return nil, fmt.Errorf("transcript stream is unavailable: %w", serverapi.ErrStreamUnavailable)
	}
	sub := &transcriptSubscription{ch: make(chan clientui.TranscriptSubscriptionMessage, sessionActivityBufferSize)}
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		sub.closeWithError(io.EOF)
		return sub, nil
	}
	id := b.nextID
	b.nextID++
	if err := sub.publish(hydration); err != nil {
		b.mu.Unlock()
		sub.closeWithError(transcriptPublishError(err))
		return sub, nil
	}
	sub.onClose = func() {
		b.mu.Lock()
		delete(b.subscribers, id)
		b.mu.Unlock()
	}
	b.subscribers[id] = sub
	b.mu.Unlock()
	return sub, nil
}

func (b *transcriptSubscriptionBroker) Publish(messages []clientui.TranscriptSubscriptionMessage) {
	if b == nil || len(messages) == 0 {
		return
	}
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return
	}
	subs := make([]*transcriptSubscription, 0, len(b.subscribers))
	for _, sub := range b.subscribers {
		subs = append(subs, sub)
	}
	b.mu.Unlock()
	for _, sub := range subs {
		for _, message := range messages {
			if err := sub.publish(message); err != nil {
				sub.closeWithError(transcriptPublishError(err))
				break
			}
		}
	}
}

func (b *transcriptSubscriptionBroker) Close(err error) {
	if b == nil {
		return
	}
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return
	}
	b.closed = true
	subs := make([]*transcriptSubscription, 0, len(b.subscribers))
	for id, sub := range b.subscribers {
		subs = append(subs, sub)
		delete(b.subscribers, id)
	}
	b.mu.Unlock()
	for _, sub := range subs {
		sub.closeWithError(err)
	}
}

func (s *transcriptSubscription) publish(message clientui.TranscriptSubscriptionMessage) error {
	if s == nil {
		return errTranscriptContractViolation("transcript subscription is nil")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.done {
		return io.EOF
	}
	message.Sequence = s.nextSeq + 1
	if err := s.contract.Validate(message); err != nil {
		return err
	}
	select {
	case s.ch <- message:
		s.nextSeq = message.Sequence
		return nil
	default:
		return serverapi.NewTranscriptStreamError(serverapi.TranscriptCloseReasonSubscriberOverflow, serverapi.ErrStreamGap)
	}
}

type transcriptContractViolation struct {
	message string
}

func errTranscriptContractViolation(message string) transcriptContractViolation {
	return transcriptContractViolation{message: strings.TrimSpace(message)}
}

func (e transcriptContractViolation) Error() string {
	if e.message == "" {
		return "transcript contract violation"
	}
	return "transcript contract violation: " + e.message
}

func transcriptPublishError(err error) error {
	if err == nil {
		return nil
	}
	var streamErr serverapi.TranscriptStreamError
	if errors.As(err, &streamErr) {
		return err
	}
	var violation transcriptContractViolation
	if errors.As(err, &violation) {
		if transcriptContractViolationsPanic {
			panic(violation)
		}
		log.Printf("transcript contract violation: %v", violation)
	}
	return serverapi.NewTranscriptStreamError(serverapi.TranscriptCloseReasonContractViolation, err)
}

type transcriptSubscriptionContract struct {
	hydrated      bool
	activeStream  *uuid.UUID
	inFlightTools map[string]struct{}
}

func (c *transcriptSubscriptionContract) Validate(message clientui.TranscriptSubscriptionMessage) error {
	if err := message.ValidatePayload(); err != nil {
		return errTranscriptContractViolation(err.Error())
	}
	if message.Sequence == 0 {
		return errTranscriptContractViolation("message sequence is required")
	}
	if !c.hydrated {
		if message.Sequence != 1 || message.Kind != clientui.TranscriptMessageHydration || message.Hydration == nil {
			return errTranscriptContractViolation(fmt.Sprintf("first message must be hydration seq=1, got kind=%q seq=%d", message.Kind, message.Sequence))
		}
		c.hydrated = true
		return c.validateHydration(*message.Hydration)
	}
	if message.Kind == clientui.TranscriptMessageHydration {
		return errTranscriptContractViolation(fmt.Sprintf("hydration repeated at seq=%d", message.Sequence))
	}
	return c.validateLiveMessage(message)
}

func (c *transcriptSubscriptionContract) validateHydration(hydration clientui.TranscriptHydration) error {
	if hydration.ActiveAssistantStream != nil {
		if hydration.ActiveAssistantStream.StreamID == uuid.Nil {
			return errTranscriptContractViolation("hydration active assistant stream has zero stream_id")
		}
		c.activeStream = cloneUUID(hydration.ActiveAssistantStream.StreamID)
	}
	for _, tool := range hydration.InFlightTools {
		if err := c.trackToolStart(tool, "hydration in-flight tool"); err != nil {
			return err
		}
	}
	for _, row := range hydration.CommittedRows {
		if err := validateCommittedRow(row, false); err != nil {
			return err
		}
	}
	return nil
}

func (c *transcriptSubscriptionContract) validateLiveMessage(message clientui.TranscriptSubscriptionMessage) error {
	switch message.Kind {
	case clientui.TranscriptMessageAssistantDelta:
		if message.AssistantDelta.StreamID == uuid.Nil {
			return errTranscriptContractViolation(fmt.Sprintf("assistant_delta at seq=%d has zero stream_id", message.Sequence))
		}
		return c.matchOrStartStream(message.AssistantDelta.StreamID, message.Sequence, "assistant_delta")
	case clientui.TranscriptMessageAssistantStreamAbort:
		if message.AssistantStreamAbort.StreamID == uuid.Nil {
			return errTranscriptContractViolation(fmt.Sprintf("assistant_stream_abort at seq=%d has zero stream_id", message.Sequence))
		}
		if err := c.matchActiveStream(message.AssistantStreamAbort.StreamID, message.Sequence, "assistant_stream_abort"); err != nil {
			return err
		}
		c.activeStream = nil
		return nil
	case clientui.TranscriptMessageToolStart:
		return c.trackToolStart(*message.ToolStart, fmt.Sprintf("tool_start at seq=%d", message.Sequence))
	case clientui.TranscriptMessageToolAbort:
		return c.trackToolTerminal(message.ToolAbort.ToolCallID, fmt.Sprintf("tool_abort at seq=%d", message.Sequence))
	case clientui.TranscriptMessageCommittedRow:
		if err := validateCommittedRow(*message.CommittedRow, true); err != nil {
			return err
		}
		if row := message.CommittedRow; row.Assistant != nil {
			if row.Assistant.StreamID != nil {
				if err := c.matchActiveStream(*row.Assistant.StreamID, message.Sequence, "committed assistant row"); err != nil {
					return err
				}
				c.activeStream = nil
			} else if c.activeStream != nil {
				return errTranscriptContractViolation(fmt.Sprintf("committed assistant row at seq=%d has nil stream_id while stream %s is active", message.Sequence, c.activeStream.String()))
			}
		}
		if row := message.CommittedRow; row.Tool != nil {
			return c.trackToolTerminal(row.Tool.ToolCallID, fmt.Sprintf("committed tool row at seq=%d", message.Sequence))
		}
	}
	return nil
}

func (c *transcriptSubscriptionContract) matchOrStartStream(streamID uuid.UUID, seq uint64, op string) error {
	if c.activeStream == nil {
		c.activeStream = cloneUUID(streamID)
		return nil
	}
	return c.matchActiveStream(streamID, seq, op)
}

func (c *transcriptSubscriptionContract) matchActiveStream(streamID uuid.UUID, seq uint64, op string) error {
	if c.activeStream == nil {
		return errTranscriptContractViolation(fmt.Sprintf("%s at seq=%d has stream_id %s with no active assistant stream", op, seq, streamID.String()))
	}
	if *c.activeStream != streamID {
		return errTranscriptContractViolation(fmt.Sprintf("%s at seq=%d has stream_id %s, active stream_id is %s", op, seq, streamID.String(), c.activeStream.String()))
	}
	return nil
}

func (c *transcriptSubscriptionContract) trackToolStart(tool clientui.TranscriptToolStart, op string) error {
	toolID := strings.TrimSpace(tool.ToolCallID)
	if toolID == "" {
		return errTranscriptContractViolation(op + " has empty tool_call_id")
	}
	if c.inFlightTools == nil {
		c.inFlightTools = make(map[string]struct{})
	}
	if _, exists := c.inFlightTools[toolID]; exists {
		return nil
	}
	c.inFlightTools[toolID] = struct{}{}
	return nil
}

func (c *transcriptSubscriptionContract) trackToolTerminal(toolCallID string, op string) error {
	toolID := strings.TrimSpace(toolCallID)
	if toolID == "" {
		return errTranscriptContractViolation(op + " has empty tool_call_id")
	}
	delete(c.inFlightTools, toolID)
	return nil
}

func validateCommittedRow(row clientui.TranscriptCommittedRow, _ bool) error {
	payloads := 0
	expectedKind := clientui.TranscriptRowKind("")
	if row.User != nil {
		payloads++
		expectedKind = clientui.TranscriptRowUser
	}
	if row.Assistant != nil {
		payloads++
		expectedKind = clientui.TranscriptRowAssistant
		if row.Assistant.StreamID != nil && *row.Assistant.StreamID == uuid.Nil {
			return errTranscriptContractViolation("committed assistant row has zero stream_id")
		}
	}
	if row.Tool != nil {
		payloads++
		expectedKind = clientui.TranscriptRowTool
		if strings.TrimSpace(row.Tool.ToolCallID) == "" {
			return errTranscriptContractViolation("committed tool row has empty tool_call_id")
		}
	}
	if row.Notice != nil {
		payloads++
		expectedKind = clientui.TranscriptRowNotice
	}
	if payloads != 1 {
		return errTranscriptContractViolation(fmt.Sprintf("committed row kind %q has %d payloads, want exactly one", row.Kind, payloads))
	}
	if row.Kind == "" {
		return errTranscriptContractViolation("committed row kind is required")
	}
	if row.Kind != expectedKind {
		return errTranscriptContractViolation(fmt.Sprintf("committed row kind %q does not match payload kind %q", row.Kind, expectedKind))
	}
	return nil
}

func cloneUUID(value uuid.UUID) *uuid.UUID {
	copied := value
	return &copied
}

func (s *transcriptSubscription) Next(ctx context.Context) (clientui.TranscriptSubscriptionMessage, error) {
	if s == nil {
		return clientui.TranscriptSubscriptionMessage{}, io.EOF
	}
	select {
	case <-ctx.Done():
		return clientui.TranscriptSubscriptionMessage{}, ctx.Err()
	case evt, ok := <-s.ch:
		if ok {
			return evt, nil
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.err != nil {
			return clientui.TranscriptSubscriptionMessage{}, serverapi.NormalizeStreamError(s.err)
		}
		return clientui.TranscriptSubscriptionMessage{}, io.EOF
	}
}

func (s *transcriptSubscription) Close() error {
	if s == nil {
		return nil
	}
	s.closeWithError(io.EOF)
	return nil
}

func (s *transcriptSubscription) closeWithError(err error) {
	if s == nil {
		return
	}
	var onClose func()
	s.mu.Lock()
	if s.done {
		s.mu.Unlock()
		return
	}
	s.done = true
	s.err = err
	close(s.ch)
	onClose = s.onClose
	s.mu.Unlock()
	if onClose != nil {
		onClose()
	}
}

var _ serverapi.SessionTranscriptSubscription = (*transcriptSubscription)(nil)
