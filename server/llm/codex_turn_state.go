package llm

import (
	"encoding/json"
	"fmt"
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

func responseHeaders(response *http.Response) http.Header {
	if response == nil {
		return nil
	}
	return response.Header
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
	parser := codexMetadataParser{raw: raw}
	var (
		headersFound bool
		candidates   []string
		invalid      bool
	)
	err := parser.parseObject(func(name string) error {
		if !strings.EqualFold(name, "headers") {
			return parser.skipValue()
		}
		if headersFound {
			invalid = true
			return parser.skipValue()
		}
		headersFound = true
		var headersInvalid bool
		candidates, headersInvalid = parser.parseTurnStateHeaders()
		if headersInvalid {
			invalid = true
		}
		return nil
	})
	if err != nil || !parser.atEnd() {
		return nil, true
	}
	if invalid {
		return nil, true
	}
	return candidates, false
}

func (p *codexMetadataParser) parseTurnStateHeaders() ([]string, bool) {
	var (
		found      bool
		candidates []string
		invalid    bool
	)
	err := p.parseObject(func(name string) error {
		if !strings.EqualFold(name, codexTurnStateHeader) {
			return p.skipValue()
		}
		if found {
			invalid = true
			return p.skipValue()
		}
		found = true
		var valueInvalid bool
		candidates, valueInvalid = p.parseTurnStateValue()
		if valueInvalid {
			candidates = nil
			invalid = true
		}
		return nil
	})
	if err != nil {
		return nil, true
	}
	if invalid {
		return nil, true
	}
	return candidates, false
}

func (p *codexMetadataParser) parseTurnStateValue() ([]string, bool) {
	p.skipSpace()
	if p.peek() == '"' {
		value, err := p.parseString()
		if err != nil {
			return nil, true
		}
		return []string{value}, false
	}
	if !p.consume('[') {
		return nil, true
	}
	values := make([]string, 0)
	p.skipSpace()
	if p.consume(']') {
		return nil, true
	}
	for {
		value, err := p.parseString()
		if err != nil {
			return nil, true
		}
		values = append(values, value)
		p.skipSpace()
		if p.consume(']') {
			return values, false
		}
		if !p.consume(',') {
			return nil, true
		}
	}
}

type codexMetadataParser struct {
	raw   string
	index int
}

func (p *codexMetadataParser) parseObject(visit func(string) error) error {
	p.skipSpace()
	if !p.consume('{') {
		return fmt.Errorf("expected JSON object")
	}
	p.skipSpace()
	if p.consume('}') {
		return nil
	}
	for {
		name, err := p.parseString()
		if err != nil {
			return err
		}
		p.skipSpace()
		if !p.consume(':') {
			return fmt.Errorf("expected JSON object member separator")
		}
		if err := visit(name); err != nil {
			return err
		}
		p.skipSpace()
		if p.consume('}') {
			return nil
		}
		if !p.consume(',') {
			return fmt.Errorf("expected JSON object member delimiter")
		}
	}
}

func (p *codexMetadataParser) skipValue() error {
	p.skipSpace()
	switch p.peek() {
	case '"':
		_, err := p.parseString()
		return err
	case '{':
		return p.parseObject(func(string) error { return p.skipValue() })
	case '[':
		p.index++
		p.skipSpace()
		if p.consume(']') {
			return nil
		}
		for {
			if err := p.skipValue(); err != nil {
				return err
			}
			p.skipSpace()
			if p.consume(']') {
				return nil
			}
			if !p.consume(',') {
				return fmt.Errorf("expected JSON array element delimiter")
			}
		}
	default:
		start := p.index
		for p.index < len(p.raw) && !strings.ContainsRune(" \t\r\n,]}", rune(p.raw[p.index])) {
			p.index++
		}
		if start == p.index || !json.Valid([]byte(p.raw[start:p.index])) {
			return fmt.Errorf("invalid JSON scalar")
		}
		return nil
	}
}

func (p *codexMetadataParser) parseString() (string, error) {
	p.skipSpace()
	if p.peek() != '"' {
		return "", fmt.Errorf("expected JSON string")
	}
	start := p.index
	p.index++
	escaped := false
	for p.index < len(p.raw) {
		current := p.raw[p.index]
		p.index++
		if escaped {
			escaped = false
			continue
		}
		switch current {
		case '\\':
			escaped = true
		case '"':
			var value string
			if err := json.Unmarshal([]byte(p.raw[start:p.index]), &value); err != nil {
				return "", err
			}
			return value, nil
		}
	}
	return "", fmt.Errorf("unterminated JSON string")
}

func (p *codexMetadataParser) atEnd() bool {
	p.skipSpace()
	return p.index == len(p.raw)
}

func (p *codexMetadataParser) skipSpace() {
	for p.index < len(p.raw) && strings.ContainsRune(" \t\r\n", rune(p.raw[p.index])) {
		p.index++
	}
}

func (p *codexMetadataParser) consume(expected byte) bool {
	if p.peek() != expected {
		return false
	}
	p.index++
	return true
}

func (p *codexMetadataParser) peek() byte {
	if p.index >= len(p.raw) {
		return 0
	}
	return p.raw[p.index]
}
