package analyzer

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
)

const (
	protocolVersion = 1
	oscPrefix       = "\x1b]777;kent-pty-checkpoint;"
	oscDataPrefix   = "777;kent-pty-checkpoint;"
)

type Kind uint8

const (
	KindScenarioStart Kind = iota + 1
	KindWindowStart
	KindWindowEnd
	KindReadyForQuit
	KindScenarioComplete
	KindInputApplied
	KindDetailInitialPageApplied
	KindScenarioFinalApplied
)

func (kind Kind) Valid() bool {
	_, ok := descriptorForKind(kind)
	return ok
}

type kindDescriptor struct {
	kind         Kind
	protocolName string
}

var kindDescriptors = [...]kindDescriptor{
	{kind: KindScenarioStart, protocolName: "ScenarioStart"},
	{kind: KindWindowStart, protocolName: "WindowStart"},
	{kind: KindWindowEnd, protocolName: "WindowEnd"},
	{kind: KindReadyForQuit, protocolName: "ReadyForQuit"},
	{kind: KindScenarioComplete, protocolName: "ScenarioComplete"},
	{kind: KindInputApplied, protocolName: "InputApplied"},
	{kind: KindDetailInitialPageApplied, protocolName: "DetailInitialPageApplied"},
	{kind: KindScenarioFinalApplied, protocolName: "ScenarioFinalApplied"},
}

func descriptorForKind(kind Kind) (kindDescriptor, bool) {
	for _, descriptor := range kindDescriptors {
		if descriptor.kind == kind {
			return descriptor, true
		}
	}
	return kindDescriptor{}, false
}

func descriptorForProtocolName(raw string) (kindDescriptor, bool) {
	for _, descriptor := range kindDescriptors {
		if descriptor.protocolName == raw {
			return descriptor, true
		}
	}
	return kindDescriptor{}, false
}

type WindowID struct {
	value uuid.UUID
}

func NewWindowID(raw string) (WindowID, error) {
	id, err := uuid.Parse(raw)
	if err != nil {
		return WindowID{}, fmt.Errorf("parse checkpoint window_id as UUID: %w", err)
	}
	if id == uuid.Nil {
		return WindowID{}, errors.New("checkpoint window_id must not be nil UUID")
	}
	if id.Version() != 4 {
		return WindowID{}, fmt.Errorf("checkpoint window_id must be UUIDv4: got version %d", id.Version())
	}
	return WindowID{value: id}, nil
}

func (id WindowID) String() string {
	return id.value.String()
}

type Marker struct {
	Sequence int
	Kind     Kind
	WindowID *WindowID
}

func (marker Marker) Validate() error {
	if marker.Sequence <= 0 {
		return fmt.Errorf("checkpoint sequence must be positive: %d", marker.Sequence)
	}
	if !marker.Kind.Valid() {
		return fmt.Errorf("checkpoint kind is invalid: %d", marker.Kind)
	}
	if (marker.Kind == KindWindowStart || marker.Kind == KindWindowEnd) && marker.WindowID == nil {
		return errors.New("window checkpoint requires window_id")
	}
	return nil
}

type wireMarker struct {
	Version  int     `json:"version"`
	Sequence int     `json:"seq"`
	Kind     string  `json:"kind"`
	WindowID *string `json:"window_id,omitempty"`
}

func Encode(marker Marker) ([]byte, error) {
	if err := marker.Validate(); err != nil {
		return nil, err
	}
	descriptor, ok := descriptorForKind(marker.Kind)
	if !ok {
		return nil, fmt.Errorf("unknown checkpoint kind %d", marker.Kind)
	}
	wire := wireMarker{
		Version:  protocolVersion,
		Sequence: marker.Sequence,
		Kind:     descriptor.protocolName,
	}
	if marker.WindowID != nil {
		raw := marker.WindowID.String()
		wire.WindowID = &raw
	}
	encoded, err := json.Marshal(wire)
	if err != nil {
		return nil, fmt.Errorf("marshal checkpoint: %w", err)
	}
	output := make([]byte, 0, len(encoded)+len(oscPrefix)+1)
	output = append(output, oscPrefix...)
	output = base64.RawURLEncoding.AppendEncode(output, encoded)
	output = append(output, '\a')
	return output, nil
}

func DecodeOSCData(data []byte) (Marker, bool, error) {
	prefix := []byte(oscDataPrefix)
	if len(data) <= len(prefix) {
		return Marker{}, false, nil
	}
	for index, want := range prefix {
		if data[index] != want {
			return Marker{}, false, nil
		}
	}
	payload := data[len(prefix):]
	decoded := make([]byte, base64.RawURLEncoding.DecodedLen(len(payload)))
	n, err := base64.RawURLEncoding.Decode(decoded, payload)
	if err != nil {
		return Marker{}, true, fmt.Errorf("decode checkpoint payload: %w", err)
	}
	var wire wireMarker
	if err := json.Unmarshal(decoded[:n], &wire); err != nil {
		return Marker{}, true, fmt.Errorf("decode checkpoint JSON: %w", err)
	}
	if wire.Version != protocolVersion {
		return Marker{}, true, fmt.Errorf("unsupported checkpoint version %d", wire.Version)
	}
	descriptor, ok := descriptorForProtocolName(wire.Kind)
	if !ok {
		return Marker{}, true, fmt.Errorf("unknown checkpoint kind %q", wire.Kind)
	}
	var windowID *WindowID
	if wire.WindowID != nil {
		if *wire.WindowID == "" {
			return Marker{}, true, errors.New("checkpoint window_id must not be empty")
		}
		parsed, err := NewWindowID(*wire.WindowID)
		if err != nil {
			return Marker{}, true, err
		}
		windowID = &parsed
	}
	marker := Marker{
		Sequence: wire.Sequence,
		Kind:     descriptor.kind,
		WindowID: windowID,
	}
	if err := marker.Validate(); err != nil {
		return Marker{}, true, err
	}
	return marker, true, nil
}
