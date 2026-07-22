package openaiwire_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"core/server/llm/openaiwire"
)

func TestFunctionCallOutputRawPreservesStructuredOutputAndLiteralHTML(t *testing.T) {
	raw, err := openaiwire.NewFunctionCallOutput(
		"call_1",
		json.RawMessage(`[{"type":"input_text","text":"<keep>&"}]`),
	)
	if err != nil {
		t.Fatalf("construct function call output: %v", err)
	}

	var got map[string]any
	if err := json.Unmarshal(raw.Bytes(), &got); err != nil {
		t.Fatalf("decode raw: %v", err)
	}
	if got["type"] != "function_call_output" || got["call_id"] != "call_1" {
		t.Fatalf("unexpected envelope: %#v", got)
	}
	output, ok := got["output"].([]any)
	if !ok || len(output) != 1 {
		t.Fatalf("expected structured output array, got %#v", got["output"])
	}
	part, ok := output[0].(map[string]any)
	if !ok || part["text"] != "<keep>&" {
		t.Fatalf("expected literal structured text, got %#v", output[0])
	}
	if len(raw.Bytes()) == 0 || bytes.Contains(raw.Bytes(), []byte(`\u003c`)) || bytes.Contains(raw.Bytes(), []byte(`\u0026`)) {
		t.Fatalf("raw output unexpectedly HTML-escaped: %s", raw.Bytes())
	}
}

func TestCustomToolOutputRawUsesProviderOutputStringSemantics(t *testing.T) {
	raw, err := openaiwire.NewCustomToolOutput("call_2", json.RawMessage(`{"answer":"<keep>&"}`))
	if err != nil {
		t.Fatalf("construct custom tool output: %v", err)
	}

	var got map[string]any
	if err := json.Unmarshal(raw.Bytes(), &got); err != nil {
		t.Fatalf("decode raw: %v", err)
	}
	if got["type"] != "custom_tool_call_output" || got["call_id"] != "call_2" {
		t.Fatalf("unexpected envelope: %#v", got)
	}
	if got["output"] != `{"answer":"<keep>&"}` {
		t.Fatalf("unexpected custom output: %#v", got["output"])
	}
}

func TestFunctionCallOutputRawStringifiesUnrecognizedArrays(t *testing.T) {
	raw, err := openaiwire.NewFunctionCallOutput("call_3", json.RawMessage(`[ 1 ]`))
	if err != nil {
		t.Fatalf("construct function call output: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw.Bytes(), &got); err != nil {
		t.Fatalf("decode raw: %v", err)
	}
	if got["output"] != `[ 1 ]` {
		t.Fatalf("unexpected output: %#v", got["output"])
	}
}

func TestFunctionCallOutputRawCanonicalizesStructuredContentLikeLivePreparation(t *testing.T) {
	raw, err := openaiwire.NewFunctionCallOutput(
		" call_3 ",
		json.RawMessage(`[{
			"type":" INPUT_IMAGE ",
			"image_url":" https://example.test/image.png ",
			"detail":"INVALID",
			"future":true
		}]`),
	)
	if err != nil {
		t.Fatalf("construct structured function output: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw.Bytes(), &got); err != nil {
		t.Fatalf("decode raw: %v", err)
	}
	if got["call_id"] != "call_3" {
		t.Fatalf("call_id = %#v, want trimmed identity", got["call_id"])
	}
	output, ok := got["output"].([]any)
	if !ok || len(output) != 1 {
		t.Fatalf("structured output = %#v", got["output"])
	}
	item, ok := output[0].(map[string]any)
	if !ok {
		t.Fatalf("structured item = %#v", output[0])
	}
	if item["type"] != "input_image" || item["image_url"] != "https://example.test/image.png" {
		t.Fatalf("canonical structured item = %#v", item)
	}
	if _, ok := item["detail"]; ok {
		t.Fatalf("invalid detail survived canonicalization: %#v", item)
	}
	if _, ok := item["future"]; ok {
		t.Fatalf("unknown field survived canonicalization: %#v", item)
	}
}

func TestFunctionCallOutputStreamingEncoderMatchesCanonicalContentSemantics(t *testing.T) {
	tests := []struct {
		name       string
		output     json.RawMessage
		wantOutput any
	}{
		{
			name:       "input text preserves exact semantic text",
			output:     json.RawMessage(`[{"type":"input_text","text":"  <keep>&\u2028  ","future":true}]`),
			wantOutput: []any{map[string]any{"type": "input_text", "text": "  <keep>&\u2028  "}},
		},
		{
			name: "image normalizes identities and detail",
			output: json.RawMessage(`[{
				"TYPE":" INPUT_IMAGE ",
				"IMAGE_URL":" https://example.test/image.png ",
				"DETAIL":" HIGH ",
				"file_id":null
			}]`),
			wantOutput: []any{map[string]any{
				"type": "input_image", "image_url": "https://example.test/image.png", "detail": "high",
			}},
		},
		{
			name: "file normalizes every optional identity",
			output: json.RawMessage(`[{
				"type":"input_file",
				"file_data":" data ",
				"file_url":" https://example.test/file ",
				"file_id":" file-1 ",
				"filename":" artifact.txt "
			}]`),
			wantOutput: []any{map[string]any{
				"type": "input_file", "file_data": "data",
				"file_url": "https://example.test/file", "file_id": "file-1",
				"filename": "artifact.txt",
			}},
		},
		{
			name:       "escaped duplicate field uses last value",
			output:     json.RawMessage(`[{"\u0074ype":"input_text","text":"old","TEXT":"new"}]`),
			wantOutput: []any{map[string]any{"type": "input_text", "text": "new"}},
		},
		{
			name:       "known field type mismatch stringifies complete array",
			output:     json.RawMessage(`[{"type":"input_text","text":42}]`),
			wantOutput: `[{"type":"input_text","text":42}]`,
		},
		{
			name:       "non-object item stringifies complete array",
			output:     json.RawMessage(`[{"type":"input_text","text":"ok"},1]`),
			wantOutput: `[{"type":"input_text","text":"ok"},1]`,
		},
		{
			name:       "invalid image identity stringifies complete array",
			output:     json.RawMessage(`[{"type":"input_image","detail":"low"}]`),
			wantOutput: `[{"type":"input_image","detail":"low"}]`,
		},
		{
			name:       "unpaired surrogate uses replacement character",
			output:     json.RawMessage(`[{"type":"input_text","text":"\ud800"}]`),
			wantOutput: []any{map[string]any{"type": "input_text", "text": "\uFFFD"}},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := openaiwire.NewFunctionCallOutput("call-structured", test.output)
			if err != nil {
				t.Fatalf("construct streamed function output: %v", err)
			}
			var decoded struct {
				Output any `json:"output"`
			}
			if err := json.Unmarshal(got.Bytes(), &decoded); err != nil {
				t.Fatalf("decode streamed provider output: %v", err)
			}
			if !reflect.DeepEqual(decoded.Output, test.wantOutput) {
				t.Fatalf("provider output = %#v, want %#v", decoded.Output, test.wantOutput)
			}
		})
	}
}

func TestFunctionCallOutputStreamingEncoderPreservesCanonicalSemanticsAboveLibraryBuffer(t *testing.T) {
	fileData := " " + strings.Repeat("x", 70<<10) + " "
	encodedFileData, err := json.Marshal(fileData)
	if err != nil {
		t.Fatalf("encode large structured file data: %v", err)
	}
	raw, err := openaiwire.NewFunctionCallOutput(
		"call-large-structured",
		json.RawMessage(`[{
			"TYPE":" input_file ",
			"file_data":`+string(encodedFileData)+`,
			"filename":" artifact.txt ",
			"future":true
		}]`),
	)
	if err != nil {
		t.Fatalf("construct large streamed function output: %v", err)
	}
	var decoded struct {
		Output []map[string]any `json:"output"`
	}
	if err := json.Unmarshal(raw.Bytes(), &decoded); err != nil {
		t.Fatalf("decode large streamed function output: %v", err)
	}
	if len(decoded.Output) != 1 ||
		decoded.Output[0]["type"] != "input_file" ||
		decoded.Output[0]["file_data"] != strings.TrimSpace(fileData) ||
		decoded.Output[0]["filename"] != "artifact.txt" {
		t.Fatalf("large canonical structured output = %#v", decoded.Output)
	}
	if _, ok := decoded.Output[0]["future"]; ok {
		t.Fatalf("unknown field survived large canonicalization: %#v", decoded.Output[0])
	}
}

func TestInputContentItemsUsesCanonicalStructuredContentNormalization(t *testing.T) {
	text := strings.Repeat("x", 512)
	encodedText, err := json.Marshal(text)
	if err != nil {
		t.Fatalf("encode structured input text: %v", err)
	}
	raw := json.RawMessage(`[{
		"TYPE":" input_text ",
		"text":` + string(encodedText) + `,
		"future":true
	}]`)

	items, ok := openaiwire.InputContentItems(raw)
	if !ok {
		t.Fatal("canonical structured input was rejected")
	}
	if len(items) != 1 || items[0].Type != "input_text" || items[0].Text != text {
		t.Fatalf("canonical structured input = %+v", items)
	}
}

func TestCustomToolOutputRawPreservesValidJSONLexicalWhitespaceInsideOutputString(t *testing.T) {
	raw, err := openaiwire.NewCustomToolOutput("call_4", json.RawMessage(`{ "answer" : true }`))
	if err != nil {
		t.Fatalf("construct custom output: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw.Bytes(), &got); err != nil {
		t.Fatalf("decode raw: %v", err)
	}
	if got["output"] != `{ "answer" : true }` {
		t.Fatalf("output = %#v, want lexical JSON text", got["output"])
	}
}

func TestOutputConstructorsPreserveBlankOutputAsEmptyString(t *testing.T) {
	for _, makeRaw := range []func() (openaiwire.Raw, error){
		func() (openaiwire.Raw, error) {
			return openaiwire.NewFunctionCallOutput("call_4", nil)
		},
		func() (openaiwire.Raw, error) {
			return openaiwire.NewCustomToolOutput("call_4", nil)
		},
	} {
		raw, err := makeRaw()
		if err != nil {
			t.Fatalf("construct blank output: %v", err)
		}
		var got map[string]any
		if err := json.Unmarshal(raw.Bytes(), &got); err != nil {
			t.Fatalf("decode raw: %v", err)
		}
		if got["output"] != "" {
			t.Fatalf("output = %#v, want empty string", got["output"])
		}
	}
}

func TestOutputConstructorsRejectInvalidInputsWithTypedErrors(t *testing.T) {
	tests := []struct {
		name string
		make func() (openaiwire.Raw, error)
	}{
		{"blank call id", func() (openaiwire.Raw, error) {
			return openaiwire.NewFunctionCallOutput(" ", json.RawMessage(`{}`))
		}},
		{"invalid json", func() (openaiwire.Raw, error) {
			return openaiwire.NewCustomToolOutput("call_1", json.RawMessage(`{`))
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := tt.make()
			var validationErr *openaiwire.ValidationError
			if !errors.As(err, &validationErr) {
				t.Fatalf("error = %T %v, want ValidationError", err, err)
			}
			if tt.name == "blank call id" && !errors.Is(err, openaiwire.ErrInvalidCallID) {
				t.Fatalf("error = %v, want ErrInvalidCallID", err)
			}
			if tt.name == "invalid json" && !errors.Is(err, openaiwire.ErrInvalidOutput) {
				t.Fatalf("error = %v, want ErrInvalidOutput", err)
			}
		})
	}
}
