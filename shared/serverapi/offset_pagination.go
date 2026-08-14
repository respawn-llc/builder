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
	if offset != nil {
		if *offset < 0 {
			return OffsetWindow{}, &offsetWindowError{
				Field:   "offset",
				Message: "offset must be non-negative",
			}
		}
	}
	if limit != nil {
		if *limit < 1 || *limit > OffsetPaginationMaxLimit {
			return OffsetWindow{}, &offsetWindowError{
				Field:   "limit",
				Message: fmt.Sprintf("limit must be between 1 and %d", OffsetPaginationMaxLimit),
			}
		}
	}
	return OffsetWindowFromValidated(offset, limit), nil
}

// OffsetWindowFromValidated materializes defaults after request validation accepts the bounds.
func OffsetWindowFromValidated(offset *int, limit *int) OffsetWindow {
	window := OffsetWindow{Limit: OffsetPaginationMaxLimit}
	if offset != nil {
		window.Offset = *offset
	}
	if limit != nil {
		window.Limit = *limit
	}
	return window
}

func TrimOffsetLookahead[T any](window OffsetWindow, items []T) ([]T, *int) {
	if len(items) <= window.Limit {
		return items, nil
	}
	nextOffset := window.Offset + window.Limit
	return items[:window.Limit], &nextOffset
}
