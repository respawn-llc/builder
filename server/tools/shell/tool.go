package shell

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

const (
	defaultLimit                       = 16_000
	headTailSize                       = 1000
	truncationBannerTemplate           = "\n\n...[Output is very large, omitted %d bytes. Consider using more targeted commands to reduce output size]...\n\n"
	backgroundTruncationBannerTemplate = "\n\n...[Omitted %d bytes, read log file for details]...\n\n"
)

func marshalNoHTMLEscape(v any) (json.RawMessage, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
}

func formatToolCallError(toolName string, err error) string {
	if err == nil {
		return fmt.Sprintf("%s failed", toolName)
	}
	if errors.Is(err, context.Canceled) {
		return fmt.Sprintf("%s failed: %s", toolName, cancellationMessage(err))
	}
	return fmt.Sprintf("%s failed: %v", toolName, err)
}

func cancellationMessage(err error) string {
	var pollErr *PollingCanceledError
	if errors.As(err, &pollErr) {
		return pollErr.Error()
	}
	return "Canceled by user"
}

func truncateWithTemplate(s string, maxLen int, bannerTemplate string) (string, bool, int) {
	if strings.TrimSpace(s) == "" {
		return "", false, 0
	}
	if len(s) <= maxLen {
		return s, false, 0
	}
	headLen, tailLen := truncationSegmentLengths(len(s), maxLen)
	removed := len(s) - headLen - tailLen
	return fmt.Sprintf("%s%s%s", s[:headLen], fmt.Sprintf(bannerTemplate, removed), s[len(s)-tailLen:]), true, removed
}

func truncationSegmentLengths(total int, maxLen int) (int, int) {
	if total <= 1 {
		return total, 0
	}
	maxPreserve := min(total-1, headTailSize*2)
	preserve := maxPreserve
	if maxLen > 0 {
		preserve = min(maxPreserve, maxLen)
	}
	if preserve < 2 {
		preserve = min(total-1, 2)
	}
	head := preserve / 2
	tail := preserve - head
	if head <= 0 {
		head = 1
		tail = preserve - head
	}
	if tail <= 0 {
		tail = 1
		head = preserve - tail
	}
	if head > headTailSize {
		head = headTailSize
	}
	if tail > headTailSize {
		tail = headTailSize
	}
	return head, tail
}
