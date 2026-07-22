package tools

import (
	"encoding/json"

	"core/shared/textutil"
)

func ErrorResult(c Call, msg string) Result {
	return ErrorResultWith(c, msg, func(v any) (json.RawMessage, error) {
		return json.Marshal(v)
	})
}

func ErrorResultWith(c Call, msg string, marshal func(any) (json.RawMessage, error)) Result {
	if marshal == nil {
		marshal = func(v any) (json.RawMessage, error) {
			return json.Marshal(v)
		}
	}
	body, err := marshal(map[string]any{"error": msg})
	if err != nil {
		body, _ = json.Marshal(map[string]any{"error": msg})
	}
	return Result{
		CallID: c.ID, Name: c.Name, Output: body, IsError: true,
		Summary: textutil.OptionalExactString(msg),
	}
}
