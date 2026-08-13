package llm

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
)

func (c *CodexDispatchContext) observeTurnStateHTTPHeaders(header http.Header) {
	if c == nil || header == nil {
		return
	}
	for _, value := range header.Values(codexTurnStateHeader) {
		c.observeTurnStateCandidate(value, codexTurnStateSourceHTTPHeader)
	}
}

func observeCodexTurnStateResponseHeaders(dispatch *CodexDispatchContext, response *http.Response, observed *bool) {
	if observed == nil || *observed || response == nil {
		return
	}
	*observed = true
	dispatch.observeTurnStateHTTPHeaders(response.Header)
}

func (c *CodexDispatchContext) observeTurnStateMetadata(raw string) {
	if c == nil {
		return
	}
	candidates, invalid := decodeCodexTurnStateMetadata(raw)
	if invalid {
		c.observeInvalidTurnStateContainer(codexTurnStateSourceMetadata)
		return
	}
	for _, candidate := range candidates {
		c.observeTurnStateCandidate(candidate, codexTurnStateSourceMetadata)
	}
}

func decodeCodexTurnStateMetadata(raw string) ([]string, bool) {
	decoder := json.NewDecoder(strings.NewReader(raw))
	nextString := func() (string, bool) {
		token, err := decoder.Token()
		if err != nil {
			return "", false
		}
		value, ok := token.(string)
		return value, ok
	}
	var skipValue func() bool
	skipValue = func() bool {
		token, err := decoder.Token()
		if err != nil {
			return false
		}
		delim, ok := token.(json.Delim)
		if !ok {
			return true
		}
		if delim != json.Delim('{') && delim != json.Delim('[') {
			return false
		}
		for decoder.More() {
			if delim == json.Delim('{') {
				if _, ok := nextString(); !ok {
					return false
				}
			}
			if !skipValue() {
				return false
			}
		}
		last, err := decoder.Token()
		return err == nil &&
			((delim == json.Delim('{') && last == json.Delim('}')) ||
				(delim == json.Delim('[') && last == json.Delim(']')))
	}
	decodeValue := func() ([]string, bool) {
		token, err := decoder.Token()
		if err != nil {
			return nil, false
		}
		if value, ok := token.(string); ok {
			return []string{value}, true
		}
		if token != json.Delim('[') {
			return nil, false
		}
		values := make([]string, 0)
		for decoder.More() {
			value, ok := nextString()
			if !ok {
				return nil, false
			}
			values = append(values, value)
		}
		last, err := decoder.Token()
		if err != nil || last != json.Delim(']') || len(values) == 0 {
			return nil, false
		}
		return values, true
	}
	decodeHeaders := func() ([]string, bool) {
		first, err := decoder.Token()
		if err != nil || first != json.Delim('{') {
			return nil, false
		}
		var (
			found      bool
			candidates []string
		)
		for decoder.More() {
			name, ok := nextString()
			if !ok {
				return nil, false
			}
			if !strings.EqualFold(name, codexTurnStateHeader) {
				if !skipValue() {
					return nil, false
				}
				continue
			}
			if found {
				return nil, false
			}
			found = true
			var valid bool
			candidates, valid = decodeValue()
			if !valid {
				return nil, false
			}
		}
		last, err := decoder.Token()
		return candidates, err == nil && last == json.Delim('}')
	}

	first, err := decoder.Token()
	if err != nil || first != json.Delim('{') {
		return nil, true
	}
	var (
		headersFound bool
		candidates   []string
	)
	for decoder.More() {
		name, ok := nextString()
		if !ok {
			return nil, true
		}
		if !strings.EqualFold(name, "headers") {
			if !skipValue() {
				return nil, true
			}
			continue
		}
		if headersFound {
			return nil, true
		}
		headersFound = true
		var valid bool
		candidates, valid = decodeHeaders()
		if !valid {
			return nil, true
		}
	}
	last, err := decoder.Token()
	if err != nil || last != json.Delim('}') {
		return nil, true
	}
	if _, err := decoder.Token(); err != io.EOF {
		return nil, true
	}
	return candidates, false
}
