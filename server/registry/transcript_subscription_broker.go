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
	"core/shared/invariant"
	"core/shared/runtimeids"
	"core/shared/serverapi"
)

const transcriptSubscriptionBufferSize = 256

type transcriptSubscriptionBroker struct {
	mu          sync.Mutex
	nextID      uint64
	closed      bool
	subscribers map[uint64]*transcriptSubscription
}

type transcriptSubscription struct {
	ch      chan clientui.TranscriptMessage
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

func (b *transcriptSubscriptionBroker) Subscribe(hydration clientui.TranscriptMessage) (*transcriptSubscription, error) {
	if b == nil {
		return nil, fmt.Errorf("transcript stream is unavailable: %w", serverapi.ErrStreamUnavailable)
	}
	sub := &transcriptSubscription{ch: make(chan clientui.TranscriptMessage, transcriptSubscriptionBufferSize)}
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

func (b *transcriptSubscriptionBroker) Publish(messages []clientui.TranscriptMessage) {
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

func (s *transcriptSubscription) publish(message clientui.TranscriptMessage) error {
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
	activeStream  *runtimeids.AssistantStreamID
	inFlightTools map[clientui.ToolCallID]struct{}
}

func (c *transcriptSubscriptionContract) Validate(message clientui.TranscriptMessage) error {
	if err := message.Validate(); err != nil {
		return errTranscriptContractViolation(err.Error())
	}
	if !c.hydrated {
		if message.Kind != clientui.TranscriptMessageHydration || message.Payload.Hydration == nil {
			return errTranscriptContractViolation(fmt.Sprintf("first message must be hydration seq=1, got kind=%q seq=%d", message.Kind, message.Sequence))
		}
		c.hydrated = true
		return c.validateHydration(*message.Payload.Hydration)
	}
	if message.Kind == clientui.TranscriptMessageHydration {
		return errTranscriptContractViolation(fmt.Sprintf("hydration repeated at seq=%d", message.Sequence))
	}
	return c.validateLiveMessage(message)
}

func (c *transcriptSubscriptionContract) validateHydration(hydration clientui.TranscriptHydration) error {
	if hydration.ActiveAssistant != nil {
		c.activeStream = cloneAssistantStreamID(hydration.ActiveAssistant.StreamID)
	}
	for _, tool := range hydration.InFlightTools {
		if err := c.trackToolStart(tool, "hydration in-flight tool"); err != nil {
			return err
		}
	}
	for _, row := range hydration.CommittedRows {
		if err := validateCommittedRow(row); err != nil {
			return err
		}
	}
	return nil
}

func (c *transcriptSubscriptionContract) validateLiveMessage(message clientui.TranscriptMessage) error {
	payload := message.Payload
	switch message.Kind {
	case clientui.TranscriptMessageAssistantDelta:
		return c.matchOrStartStream(payload.AssistantDelta.StreamID, message.Sequence, "assistant_delta")
	case clientui.TranscriptMessageAssistantStreamAbort:
		if err := c.matchActiveStream(payload.AssistantStreamAbort.StreamID, message.Sequence, "assistant_stream_abort"); err != nil {
			return err
		}
		c.activeStream = nil
		return nil
	case clientui.TranscriptMessageToolStart:
		return c.trackToolStart(*payload.ToolStart, fmt.Sprintf("tool_start at seq=%d", message.Sequence))
	case clientui.TranscriptMessageToolAbort:
		return c.trackToolTerminal(payload.ToolAbort.ToolCallID, fmt.Sprintf("tool_abort at seq=%d", message.Sequence))
	case clientui.TranscriptMessageCommittedRow:
		if err := validateCommittedRow(*payload.CommittedRow); err != nil {
			return err
		}
		if row := payload.CommittedRow; row.Assistant != nil {
			if row.Assistant.StreamID != nil {
				if err := c.matchActiveStream(*row.Assistant.StreamID, message.Sequence, "committed assistant row"); err != nil {
					return err
				}
				c.activeStream = nil
			} else if c.activeStream != nil {
				return errTranscriptContractViolation(fmt.Sprintf("committed assistant row at seq=%d has nil stream_id while stream %s is active", message.Sequence, c.activeStream.String()))
			}
		}
		if row := payload.CommittedRow; row.Tool != nil {
			return c.trackToolTerminal(row.Tool.ToolCallID, fmt.Sprintf("committed tool row at seq=%d", message.Sequence))
		}
	}
	return nil
}

func (c *transcriptSubscriptionContract) matchOrStartStream(streamID runtimeids.AssistantStreamID, seq uint64, op string) error {
	if c.activeStream == nil {
		c.activeStream = cloneAssistantStreamID(streamID)
		return nil
	}
	return c.matchActiveStream(streamID, seq, op)
}

func (c *transcriptSubscriptionContract) matchActiveStream(streamID runtimeids.AssistantStreamID, seq uint64, op string) error {
	if c.activeStream == nil {
		return errTranscriptContractViolation(fmt.Sprintf("%s at seq=%d has stream_id %s with no active assistant stream", op, seq, streamID.String()))
	}
	if *c.activeStream != streamID {
		return errTranscriptContractViolation(fmt.Sprintf("%s at seq=%d has stream_id %s, active stream_id is %s", op, seq, streamID.String(), c.activeStream.String()))
	}
	return nil
}

func (c *transcriptSubscriptionContract) trackToolStart(tool clientui.TranscriptToolStart, op string) error {
	toolID := clientui.ToolCallID(strings.TrimSpace(string(tool.ToolCallID)))
	if toolID == "" {
		return errTranscriptContractViolation(op + " has empty tool_call_id")
	}
	if c.inFlightTools == nil {
		c.inFlightTools = make(map[clientui.ToolCallID]struct{})
	}
	if _, exists := c.inFlightTools[toolID]; exists {
		return nil
	}
	c.inFlightTools[toolID] = struct{}{}
	return nil
}

func (c *transcriptSubscriptionContract) trackToolTerminal(toolCallID clientui.ToolCallID, op string) error {
	toolID := clientui.ToolCallID(strings.TrimSpace(string(toolCallID)))
	if toolID == "" {
		return errTranscriptContractViolation(op + " has empty tool_call_id")
	}
	delete(c.inFlightTools, toolID)
	return nil
}

func validateCommittedRow(row clientui.TranscriptCommittedRow) error {
	if err := invariant.ValidateTranscriptCommittedRow(row); err != nil {
		return errTranscriptContractViolation(err.Error())
	}
	return nil
}

func cloneAssistantStreamID(value runtimeids.AssistantStreamID) *runtimeids.AssistantStreamID {
	copied := value
	return &copied
}

func (s *transcriptSubscription) Next(ctx context.Context) (clientui.TranscriptMessage, error) {
	if s == nil {
		return clientui.TranscriptMessage{}, io.EOF
	}
	select {
	case <-ctx.Done():
		return clientui.TranscriptMessage{}, ctx.Err()
	case evt, ok := <-s.ch:
		if ok {
			return evt, nil
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.err != nil {
			return clientui.TranscriptMessage{}, serverapi.NormalizeStreamError(s.err)
		}
		return clientui.TranscriptMessage{}, io.EOF
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

var _ serverapi.TranscriptSubscription = (*transcriptSubscription)(nil)
