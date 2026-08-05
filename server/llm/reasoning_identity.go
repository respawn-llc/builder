package llm

import (
	"fmt"
	"strings"

	"core/shared/textutil"
)

func CloneReasoningSourceCoordinate(coordinate *ReasoningSourceCoordinate) *ReasoningSourceCoordinate {
	if coordinate == nil {
		return nil
	}
	return &ReasoningSourceCoordinate{
		OutputIndex: textutil.Pointer(coordinate.OutputIndex),
		PartIndex:   textutil.Pointer(coordinate.PartIndex),
	}
}

func CloneReasoningItemIdentity(identity *ReasoningItemIdentity) *ReasoningItemIdentity {
	if identity == nil {
		return nil
	}
	return &ReasoningItemIdentity{
		ItemID:    identity.ItemID,
		PartIndex: textutil.Pointer(identity.PartIndex),
	}
}

func ReasoningItemIdentityEqual(left, right *ReasoningItemIdentity) bool {
	if left == nil || right == nil || left.PartIndex == nil || right.PartIndex == nil {
		return left == right
	}
	return strings.TrimSpace(left.ItemID) == strings.TrimSpace(right.ItemID) &&
		*left.PartIndex == *right.PartIndex
}

func ReasoningItemIdentityAlias(identity ReasoningItemIdentity) (string, error) {
	if err := identity.Validate(); err != nil {
		return "", err
	}
	return fmt.Sprintf("%s:%d", strings.TrimSpace(identity.ItemID), *identity.PartIndex), nil
}

func ReasoningCoordinate(outputIndex, partIndex int64) (ReasoningSourceCoordinate, error) {
	coordinate := ReasoningSourceCoordinate{
		OutputIndex: &outputIndex,
		PartIndex:   &partIndex,
	}
	if err := coordinate.Validate(); err != nil {
		return ReasoningSourceCoordinate{}, err
	}
	return coordinate, nil
}

func ReasoningItemIdentityFromProvider(itemID string, partIndex int64) (*ReasoningItemIdentity, error) {
	itemID = strings.TrimSpace(itemID)
	if itemID == "" {
		return nil, nil
	}
	identity := &ReasoningItemIdentity{ItemID: itemID, PartIndex: &partIndex}
	if err := identity.Validate(); err != nil {
		return nil, err
	}
	return identity, nil
}
