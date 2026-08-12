package serverapi

import "fmt"

const OffsetPaginationMaxLimit = 100

type OffsetWindow struct {
	Offset int
	Limit  int
}

type offsetWindowError struct {
	Field   string
	Message string
}

func (e *offsetWindowError) Error() string {
	return e.Message
}

func ResolveOffsetWindow(offset *int, limit *int) (OffsetWindow, error) {
	resolvedOffset := 0
	if offset != nil {
		if *offset < 0 {
			return OffsetWindow{}, &offsetWindowError{
				Field:   "offset",
				Message: "offset must be non-negative",
			}
		}
		resolvedOffset = *offset
	}
	resolvedLimit := OffsetPaginationMaxLimit
	if limit != nil {
		if *limit < 1 || *limit > OffsetPaginationMaxLimit {
			return OffsetWindow{}, &offsetWindowError{
				Field:   "limit",
				Message: fmt.Sprintf("limit must be between 1 and %d", OffsetPaginationMaxLimit),
			}
		}
		resolvedLimit = *limit
	}
	return OffsetWindow{Offset: resolvedOffset, Limit: resolvedLimit}, nil
}

func TrimOffsetLookahead[T any](window OffsetWindow, items []T) ([]T, *int) {
	if len(items) <= window.Limit {
		return items, nil
	}
	nextOffset := window.Offset + window.Limit
	return items[:window.Limit], &nextOffset
}
