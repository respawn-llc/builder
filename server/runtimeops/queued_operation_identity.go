package runtimeops

import (
	"fmt"

	"core/shared/clientui"
)

func (l *sessionLedger) operationKey(ref clientui.RuntimeOperationRef) string {
	key := ref.Key()
	if l == nil || ref.Kind != clientui.RuntimeOperationKindQueuedMessage {
		return key
	}
	if ref.QueueItemID == nil {
		return key
	}
	identity := l.queuedByQueueItemID[*ref.QueueItemID]
	if identity == nil {
		return key
	}
	return identity.operationKey
}

func (l *sessionLedger) bindQueuedOperation(ref clientui.RuntimeOperationRef) error {
	if l == nil {
		return fmt.Errorf("runtime operation ledger is required")
	}
	if err := ref.Validate(); err != nil {
		return err
	}
	if ref.Kind != clientui.RuntimeOperationKindQueuedMessage || ref.QueueItemID == nil {
		return fmt.Errorf("queued-message runtime operation requires queue item id")
	}
	clientRequestID := ref.ClientRequestID
	queueItemID := *ref.QueueItemID
	byClientRequest := l.queuedByClientRequestID[clientRequestID]
	byQueueItem := l.queuedByQueueItemID[queueItemID]
	if byClientRequest != nil && byClientRequest.queueItemID != queueItemID {
		return fmt.Errorf("queued-message client request %q is already bound to queue item %q, not %q", clientRequestID.String(), byClientRequest.queueItemID.String(), queueItemID.String())
	}
	if byQueueItem != nil && byQueueItem.clientRequestID != clientRequestID {
		return fmt.Errorf("queued-message queue item %q is already bound to client request %q, not %q", queueItemID.String(), byQueueItem.clientRequestID.String(), clientRequestID.String())
	}
	if byClientRequest != nil && byQueueItem != nil && byClientRequest != byQueueItem {
		return fmt.Errorf("queued-message identity indexes disagree for client request %q and queue item %q", clientRequestID.String(), queueItemID.String())
	}
	identity := byClientRequest
	if identity == nil {
		identity = byQueueItem
	}
	if identity == nil {
		identity = &queuedOperationIdentity{
			clientRequestID: clientRequestID,
			queueItemID:     queueItemID,
			operationKey: (clientui.RuntimeOperationRef{
				Kind:            clientui.RuntimeOperationKindQueuedMessage,
				ClientRequestID: clientRequestID,
			}).Key(),
		}
	}
	l.queuedByClientRequestID[clientRequestID] = identity
	l.queuedByQueueItemID[queueItemID] = identity
	l.queuedByOperationKey[identity.operationKey] = identity
	return nil
}

func (l *sessionLedger) removeQueuedOperationIdentity(operationKey string) {
	if l == nil {
		return
	}
	identity := l.queuedByOperationKey[operationKey]
	if identity == nil {
		return
	}
	delete(l.queuedByOperationKey, operationKey)
	if l.queuedByClientRequestID[identity.clientRequestID] == identity {
		delete(l.queuedByClientRequestID, identity.clientRequestID)
	}
	if l.queuedByQueueItemID[identity.queueItemID] == identity {
		delete(l.queuedByQueueItemID, identity.queueItemID)
	}
}

func runtimeFeedOperationRef(ledger *sessionLedger, ref clientui.RuntimeOperationRef) (clientui.RuntimeOperationRef, string, error) {
	if err := ref.Validate(); err != nil {
		return clientui.RuntimeOperationRef{}, "", err
	}
	operation := ref
	if ref.Kind == clientui.RuntimeOperationKindQueuedMessage && ref.QueueItemID != nil {
		if ledger == nil {
			return clientui.RuntimeOperationRef{}, "", fmt.Errorf("queued-message queue item %q has no client request identity", ref.QueueItemID.String())
		}
		identity := ledger.queuedByQueueItemID[*ref.QueueItemID]
		if identity == nil {
			return clientui.RuntimeOperationRef{}, "", fmt.Errorf("queued-message queue item %q has no client request identity", ref.QueueItemID.String())
		}
		operation.ClientRequestID = identity.clientRequestID
		operation.QueueItemID = &identity.queueItemID
	} else if ref.Kind == clientui.RuntimeOperationKindQueuedMessage && ledger != nil {
		clientRequestID := ref.ClientRequestID
		if ref.Kind == clientui.RuntimeOperationKindQueuedMessage && ledger != nil {
			if identity := ledger.queuedByClientRequestID[clientRequestID]; identity != nil {
				operation.QueueItemID = &identity.queueItemID
			}
		}
	}
	if err := operation.Validate(); err != nil {
		return clientui.RuntimeOperationRef{}, "", err
	}
	key := (clientui.RuntimeOperationRef{
		Kind:            ref.Kind,
		ClientRequestID: operation.ClientRequestID,
	}).Key()
	return operation, key, nil
}
