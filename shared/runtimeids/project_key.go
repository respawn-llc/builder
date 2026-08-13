package runtimeids

import (
	"errors"
	"strings"
)

var ErrInvalidProjectKey = errors.New("invalid project key")

type ProjectKey struct {
	value string
}

func ParseProjectKey(raw string) (ProjectKey, error) {
	value := strings.ToUpper(strings.TrimSpace(raw))
	if !validProjectKey(value) {
		return ProjectKey{}, ErrInvalidProjectKey
	}
	return ProjectKey{value: value}, nil
}

func (key ProjectKey) String() string {
	if key.value == "" {
		panic("invalid empty Project Key")
	}
	return key.value
}

func validProjectKey(value string) bool {
	if len(value) < 2 || len(value) > 8 {
		return false
	}
	for index, char := range value {
		if index == 0 {
			if char < 'A' || char > 'Z' {
				return false
			}
			continue
		}
		if (char < 'A' || char > 'Z') && (char < '0' || char > '9') {
			return false
		}
	}
	return true
}
