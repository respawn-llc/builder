package tools

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

type EditInput struct {
	Path       string
	OldString  string
	NewString  string
	ReplaceAll bool
}

func ParseEditInput(raw json.RawMessage) (EditInput, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return EditInput{}, fmt.Errorf("expected JSON object input.")
	}
	if fields == nil {
		return EditInput{}, fmt.Errorf("expected JSON object input.")
	}
	path, err := pickEditString(fields, "path", "file_path", "filePath")
	if err != nil {
		return EditInput{}, err
	}
	oldString, err := pickEditString(fields, "old_string", "oldString", "oldText")
	if err != nil {
		return EditInput{}, err
	}
	newString, err := pickEditString(fields, "new_string", "newString", "newText")
	if err != nil {
		return EditInput{}, err
	}
	replaceAll, err := pickEditBool(fields, "replace_all", "replaceAll")
	if err != nil {
		return EditInput{}, err
	}
	in := EditInput{Path: path, OldString: oldString, NewString: newString, ReplaceAll: replaceAll}
	if strings.TrimSpace(in.Path) == "" {
		return in, fmt.Errorf("path is required.")
	}
	if in.OldString == in.NewString {
		return in, fmt.Errorf("old_string and new_string must be different.")
	}
	return in, nil
}

func pickEditString(fields map[string]json.RawMessage, names ...string) (string, error) {
	var selected []byte
	found := ""
	for _, name := range names {
		raw, ok := fields[name]
		if !ok {
			continue
		}
		var value string
		if err := json.Unmarshal(raw, &value); err != nil {
			return "", fmt.Errorf("%s must be a string.", name)
		}
		encoded, _ := json.Marshal(value)
		if found != "" && !bytes.Equal(selected, encoded) {
			return "", fmt.Errorf("conflicting aliases for %s.", names[0])
		}
		found = name
		selected = encoded
	}
	if found == "" {
		return "", fmt.Errorf("%s is required.", names[0])
	}
	var value string
	_ = json.Unmarshal(selected, &value)
	return value, nil
}

func pickEditBool(fields map[string]json.RawMessage, names ...string) (bool, error) {
	var selected []byte
	found := ""
	for _, name := range names {
		raw, ok := fields[name]
		if !ok {
			continue
		}
		var value bool
		if err := json.Unmarshal(raw, &value); err != nil {
			return false, fmt.Errorf("%s must be a boolean.", name)
		}
		encoded, _ := json.Marshal(value)
		if found != "" && !bytes.Equal(selected, encoded) {
			return false, fmt.Errorf("conflicting aliases for %s.", names[0])
		}
		found = name
		selected = encoded
	}
	if found == "" {
		return false, nil
	}
	var value bool
	if err := json.Unmarshal(selected, &value); err != nil {
		return false, fmt.Errorf("decode %s: %w", names[0], err)
	}
	return value, nil
}
