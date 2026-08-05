package llm

import (
	"fmt"
	"strings"

	"core/shared/textutil"
	"github.com/openai/openai-go/v3/responses"
)

type reasoningCoordinate struct {
	output int64
	part   int64
}

func validateReasoningStreamEvent(evt responses.ResponseStreamEventUnion) error {
	switch evt.Type {
	case "response.reasoning_summary_text.delta",
		"response.reasoning_summary_text.done",
		"response.reasoning_summary_part.added",
		"response.reasoning_summary_part.done":
		if !evt.JSON.OutputIndex.Valid() || !evt.JSON.SummaryIndex.Valid() {
			return fmt.Errorf("reasoning stream event requires output and summary indexes")
		}
	case "response.output_item.added", "response.output_item.done":
		if evt.Item.Type == "reasoning" && !evt.JSON.OutputIndex.Valid() {
			return fmt.Errorf("reasoning output item event requires an output index")
		}
	}
	return nil
}

func reasoningSourceCoordinate(outputIndex, partIndex int64) (reasoningCoordinate, error) {
	coordinate, err := ReasoningCoordinate(outputIndex, partIndex)
	if err != nil {
		return reasoningCoordinate{}, err
	}
	return reasoningCoordinate{output: *coordinate.OutputIndex, part: *coordinate.PartIndex}, nil
}

func reasoningCoordinateValue(coordinate *ReasoningSourceCoordinate) (reasoningCoordinate, bool) {
	if coordinate == nil || coordinate.OutputIndex == nil || coordinate.PartIndex == nil {
		return reasoningCoordinate{}, false
	}
	return reasoningCoordinate{output: *coordinate.OutputIndex, part: *coordinate.PartIndex}, true
}

func reasoningItemIdentity(itemID string, partIndex int64) (*ReasoningItemIdentity, error) {
	itemID = strings.TrimSpace(itemID)
	if itemID == "" {
		if partIndex < 0 {
			return nil, fmt.Errorf("reasoning item identity part index must be nonnegative")
		}
		return nil, nil
	}
	return ReasoningItemIdentityFromProvider(itemID, partIndex)
}

func mergeReasoningEntries(primary, secondary []ReasoningEntry) ([]ReasoningEntry, error) {
	out := make([]ReasoningEntry, 0, len(primary)+len(secondary))
	seenCoordinates := make(map[reasoningCoordinate]int, len(primary)+len(secondary))
	seenAliases := make(map[string]reasoningCoordinate, len(primary)+len(secondary))
	appendEntries := func(entries []ReasoningEntry) error {
		seenInputCoordinates := make(map[reasoningCoordinate]struct{}, len(entries))
		for _, entry := range entries {
			role, present := textutil.OptionalTrimmed(entry.Role)
			text := strings.TrimSpace(entry.Text)
			if !present || text == "" {
				continue
			}
			entry.Role = textutil.Value(role)
			entry.Text = text
			if identity := entry.ItemIdentity; identity != nil {
				if err := identity.Validate(); err != nil {
					return err
				}
				alias, err := ReasoningItemIdentityAlias(*identity)
				if err != nil {
					return err
				}
				if coordinate, ok := reasoningCoordinateValue(entry.SourceCoordinate); ok {
					if existing, exists := seenAliases[alias]; exists && existing != coordinate {
						return fmt.Errorf("reasoning item identity %q aliases multiple source coordinates", alias)
					}
					seenAliases[alias] = coordinate
				}
			}
			if coordinate, ok := reasoningCoordinateValue(entry.SourceCoordinate); ok {
				if _, duplicate := seenInputCoordinates[coordinate]; duplicate {
					return fmt.Errorf("completed reasoning response repeats source coordinate output=%d part=%d", coordinate.output, coordinate.part)
				}
				seenInputCoordinates[coordinate] = struct{}{}
				if index, exists := seenCoordinates[coordinate]; exists {
					if entry.ItemIdentity != nil &&
						out[index].ItemIdentity != nil &&
						!ReasoningItemIdentityEqual(out[index].ItemIdentity, entry.ItemIdentity) {
						return fmt.Errorf("reasoning source coordinate received conflicting provider item identity")
					}
					if out[index].Text == "" {
						out[index].Text = entry.Text
					}
					if out[index].ItemIdentity == nil {
						out[index].ItemIdentity = CloneReasoningItemIdentity(entry.ItemIdentity)
					}
					continue
				}
				seenCoordinates[coordinate] = len(out)
			}
			out = append(out, ReasoningEntry{
				Role:             entry.Role,
				Text:             entry.Text,
				SourceCoordinate: CloneReasoningSourceCoordinate(entry.SourceCoordinate),
				ItemIdentity:     CloneReasoningItemIdentity(entry.ItemIdentity),
			})
		}
		return nil
	}
	if err := appendEntries(primary); err != nil {
		return nil, err
	}
	if err := appendEntries(secondary); err != nil {
		return nil, err
	}
	return out, nil
}

func mergeReasoningItems(primary, secondary []ReasoningItem) []ReasoningItem {
	out := make([]ReasoningItem, 0, len(primary)+len(secondary))
	seen := make(map[string]struct{}, len(primary)+len(secondary))
	appendItems := func(items []ReasoningItem) {
		for _, item := range items {
			id := strings.TrimSpace(item.ID)
			encrypted := strings.TrimSpace(item.EncryptedContent)
			if id == "" || encrypted == "" {
				continue
			}
			if _, exists := seen[id]; exists {
				continue
			}
			seen[id] = struct{}{}
			out = append(out, ReasoningItem{ID: id, EncryptedContent: encrypted})
		}
	}
	appendItems(primary)
	appendItems(secondary)
	return out
}

type reasoningAccumulator struct {
	order         []reasoningCoordinate
	items         map[reasoningCoordinate]*ReasoningEntry
	reasoningIDs  []string
	reasoningByID map[string]ReasoningItem
	aliases       map[string]reasoningCoordinate
	err           error
}

func newReasoningAccumulator() *reasoningAccumulator {
	return &reasoningAccumulator{
		order:         make([]reasoningCoordinate, 0, 8),
		items:         make(map[reasoningCoordinate]*ReasoningEntry, 8),
		reasoningIDs:  make([]string, 0, 4),
		reasoningByID: make(map[string]ReasoningItem, 4),
		aliases:       make(map[string]reasoningCoordinate, 8),
	}
}

func (a *reasoningAccumulator) ensure(role string, coordinate reasoningCoordinate, identity *ReasoningItemIdentity) *ReasoningEntry {
	role = strings.TrimSpace(role)
	if role == "" || a == nil || a.err != nil {
		return nil
	}
	if identity != nil {
		alias, err := ReasoningItemIdentityAlias(*identity)
		if err != nil {
			a.err = err
			return nil
		}
		if item, ok := a.items[coordinate]; ok && item.ItemIdentity != nil &&
			!ReasoningItemIdentityEqual(item.ItemIdentity, identity) {
			a.err = fmt.Errorf("reasoning source coordinate received conflicting provider item identity")
			return nil
		}
		if existing, ok := a.aliases[alias]; ok && existing != coordinate {
			a.err = fmt.Errorf("reasoning item identity %q aliases multiple source coordinates", alias)
			return nil
		}
		a.aliases[alias] = coordinate
	}
	if item, ok := a.items[coordinate]; ok {
		if item.ItemIdentity == nil {
			item.ItemIdentity = CloneReasoningItemIdentity(identity)
		}
		return item
	}
	outputIndex := coordinate.output
	partIndex := coordinate.part
	entry := &ReasoningEntry{
		Role: textutil.Value(role),
		SourceCoordinate: &ReasoningSourceCoordinate{
			OutputIndex: &outputIndex,
			PartIndex:   &partIndex,
		},
		ItemIdentity: CloneReasoningItemIdentity(identity),
	}
	a.items[coordinate] = entry
	a.order = append(a.order, coordinate)
	return entry
}

func (a *reasoningAccumulator) Append(role string, coordinate reasoningCoordinate, identity *ReasoningItemIdentity, delta string) {
	if delta == "" {
		return
	}
	entry := a.ensure(role, coordinate, identity)
	if entry == nil {
		return
	}
	entry.Text += delta
}

func (a *reasoningAccumulator) Set(role string, coordinate reasoningCoordinate, identity *ReasoningItemIdentity, text string) {
	entry := a.ensure(role, coordinate, identity)
	if entry == nil {
		return
	}
	entry.Text = text
}

func (a *reasoningAccumulator) Current(role string, coordinate reasoningCoordinate) *ReasoningEntry {
	if a == nil || a.err != nil {
		return nil
	}
	return a.items[coordinate]
}

func (a *reasoningAccumulator) Entries() []ReasoningEntry {
	if a == nil {
		return nil
	}
	out := make([]ReasoningEntry, 0, len(a.order))
	for _, coordinate := range a.order {
		entry, ok := a.items[coordinate]
		if !ok {
			continue
		}
		text := strings.TrimSpace(entry.Text)
		if text == "" {
			continue
		}
		out = append(out, ReasoningEntry{
			Role:             entry.Role,
			Text:             text,
			SourceCoordinate: CloneReasoningSourceCoordinate(entry.SourceCoordinate),
			ItemIdentity:     CloneReasoningItemIdentity(entry.ItemIdentity),
		})
	}
	return out
}

func (a *reasoningAccumulator) UpsertReasoningItem(item responses.ResponseOutputItemUnion, outputIndex int64) {
	if a == nil || item.Type != "reasoning" || a.err != nil {
		return
	}
	reasoningItem := item.AsReasoning()
	id := strings.TrimSpace(reasoningItem.ID)
	if id == "" {
		return
	}
	for idx, summary := range reasoningItem.Summary {
		partIndex := int64(idx)
		coordinate, coordinateErr := reasoningSourceCoordinate(outputIndex, partIndex)
		if coordinateErr != nil {
			a.err = coordinateErr
			return
		}
		identity, identityErr := reasoningItemIdentity(id, partIndex)
		if identityErr != nil {
			a.err = identityErr
			return
		}
		a.Set(reasoningRoleSummary, coordinate, identity, summary.Text)
		if a.err != nil {
			return
		}
	}
	encrypted := strings.TrimSpace(reasoningItem.EncryptedContent)
	if encrypted == "" {
		return
	}
	if _, exists := a.reasoningByID[id]; !exists {
		a.reasoningIDs = append(a.reasoningIDs, id)
	}
	a.reasoningByID[id] = ReasoningItem{ID: id, EncryptedContent: encrypted}
}

func (a *reasoningAccumulator) Items() []ReasoningItem {
	if a == nil {
		return nil
	}
	out := make([]ReasoningItem, 0, len(a.reasoningIDs))
	for _, id := range a.reasoningIDs {
		item, ok := a.reasoningByID[id]
		if !ok {
			continue
		}
		if strings.TrimSpace(item.ID) == "" || strings.TrimSpace(item.EncryptedContent) == "" {
			continue
		}
		out = append(out, item)
	}
	return out
}
