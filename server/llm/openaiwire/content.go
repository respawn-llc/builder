package openaiwire

import (
	"encoding/json"
)

type InputContent struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	ImageURL string `json:"image_url,omitempty"`
	Detail   string `json:"detail,omitempty"`
	FileID   string `json:"file_id,omitempty"`
	FileData string `json:"file_data,omitempty"`
	FileURL  string `json:"file_url,omitempty"`
	Filename string `json:"filename,omitempty"`
}

func InputContentItems(raw json.RawMessage) ([]InputContent, bool) {
	if len(raw) == 0 {
		return nil, false
	}
	items := make([]InputContent, 0)
	err := scanInputContentSource(
		bytesJSONSource{value: raw},
		heapScratchAllocator{},
		func(reader JSONSourceReader, item canonicalInputContentItem) error {
			if item.kind == "" {
				return &ValidationError{Kind: ValidationInvalidOutput}
			}
			materialized, err := materializeInputContentItem(reader, item)
			if err != nil {
				return err
			}
			items = append(items, materialized)
			return nil
		},
	)
	if err != nil || len(items) == 0 {
		return nil, false
	}
	return items, true
}

func materializeInputContentItem(
	reader JSONSourceReader,
	item canonicalInputContentItem,
) (InputContent, error) {
	value := func(slot int) (string, error) {
		return materializeJSONStringWindow(
			reader,
			item.values[slot],
			item.windows[slot],
			0,
		)
	}
	result := InputContent{Type: item.kind, Detail: item.detail}
	var err error
	if result.Text, err = value(inputContentTextSlot); err != nil {
		return InputContent{}, err
	}
	if result.ImageURL, err = value(inputContentImageURLSlot); err != nil {
		return InputContent{}, err
	}
	if result.FileID, err = value(inputContentFileIDSlot); err != nil {
		return InputContent{}, err
	}
	if result.FileData, err = value(inputContentFileDataSlot); err != nil {
		return InputContent{}, err
	}
	if result.FileURL, err = value(inputContentFileURLSlot); err != nil {
		return InputContent{}, err
	}
	if result.Filename, err = value(inputContentFilenameSlot); err != nil {
		return InputContent{}, err
	}
	return result, nil
}
