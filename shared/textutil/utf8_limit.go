package textutil

import (
	"fmt"
	"unicode/utf8"
)

const MarkdownSummaryLimitBytes = 4 * 1024

func LimitUTF8Bytes(value string, limit int) (string, bool, error) {
	if limit <= 0 {
		return "", false, fmt.Errorf("UTF-8 byte limit must be positive")
	}
	if !utf8.ValidString(value) {
		return "", false, fmt.Errorf("value must be valid UTF-8")
	}
	if len(value) <= limit {
		return value, false, nil
	}
	for end := limit; end > 0; end-- {
		if utf8.ValidString(value[:end]) {
			return value[:end], true, nil
		}
	}
	return "", true, nil
}

func ValidateUTF8ByteLimit(value string, limit int) error {
	_, truncated, err := LimitUTF8Bytes(value, limit)
	if err != nil {
		return err
	}
	if truncated {
		return fmt.Errorf("value exceeds %d bytes", limit)
	}
	return nil
}
