package transcriptdiag

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"maps"
	"slices"
	"strings"
)

func EnabledFromEnv(getenv func(string) string) bool {
	if getenv == nil {
		return false
	}
	value := strings.TrimSpace(getenv("KENT_TRANSCRIPT_DIAGNOSTICS"))
	if value == "" {
		return false
	}
	switch strings.ToLower(value) {
	case "0", "false", "off", "no":
		return false
	default:
		return true
	}
}

func Enabled(debug bool, getenv func(string) string) bool {
	if debug {
		return true
	}
	return EnabledFromEnv(getenv)
}

func Digest(parts ...string) string {
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x1d")))
	return hex.EncodeToString(sum[:8])
}

func FormatLine(name string, fields map[string]string) string {
	keys := slices.Sorted(maps.Keys(fields))
	parts := make([]string, 0, len(keys)+1)
	parts = append(parts, strings.TrimSpace(name))
	for _, key := range keys {
		value := strings.TrimSpace(fields[key])
		if value == "" {
			continue
		}
		if strings.ContainsAny(value, " \t\n\r\"") {
			parts = append(parts, fmt.Sprintf("%s=%q", key, value))
		} else {
			parts = append(parts, key+"="+value)
		}
	}
	return strings.Join(parts, " ")
}
