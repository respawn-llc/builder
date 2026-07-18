package gitref

import (
	"errors"
	"fmt"
	"strings"
)

func validateComponent(component string) error {
	if component == "" {
		return errors.New("Git ref components must not be empty")
	}
	if component[0] == '.' || component[len(component)-1] == '.' || endsWithDotLock(component) {
		return fmt.Errorf("invalid Git ref component %q", component)
	}
	previous := byte(0)
	for index := 0; index < len(component); index++ {
		current := component[index]
		if current <= ' ' || current == 0x7f {
			return fmt.Errorf("invalid control or whitespace byte in Git ref component %q", component)
		}
		switch current {
		case '~', '^', ':', '?', '*', '[', '\\':
			return fmt.Errorf("invalid byte %q in Git ref component %q", current, component)
		}
		if previous == '.' && current == '.' {
			return fmt.Errorf("Git ref component %q contains consecutive dots", component)
		}
		if previous == '@' && current == '{' {
			return fmt.Errorf("Git ref component %q contains an invalid reflog marker", component)
		}
		previous = current
	}
	return nil
}

func endsWithDotLock(component string) bool {
	length := len(component)
	return length >= 5 &&
		component[length-5] == '.' &&
		component[length-4] == 'l' &&
		component[length-3] == 'o' &&
		component[length-2] == 'c' &&
		component[length-1] == 'k'
}

func joinComponents(components []string) string {
	var name strings.Builder
	for index, component := range components {
		if index > 0 {
			name.WriteByte('/')
		}
		name.WriteString(component)
	}
	return name.String()
}
