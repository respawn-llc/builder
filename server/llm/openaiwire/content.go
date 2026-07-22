package openaiwire

import (
	"encoding/json"
	"strings"
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
	var items []InputContent
	if err := json.Unmarshal(raw, &items); err != nil || len(items) == 0 {
		return nil, false
	}
	for index := range items {
		item := &items[index]
		item.Type = strings.ToLower(strings.TrimSpace(item.Type))
		switch item.Type {
		case "input_text":
		case "input_image":
			item.ImageURL = strings.TrimSpace(item.ImageURL)
			if item.ImageURL == "" {
				return nil, false
			}
			item.Detail = strings.ToLower(strings.TrimSpace(item.Detail))
			switch item.Detail {
			case "low", "high", "auto":
			default:
				item.Detail = ""
			}
		case "input_file":
			item.FileID = strings.TrimSpace(item.FileID)
			item.FileData = strings.TrimSpace(item.FileData)
			item.FileURL = strings.TrimSpace(item.FileURL)
			item.Filename = strings.TrimSpace(item.Filename)
			if item.FileID == "" && item.FileData == "" && item.FileURL == "" {
				return nil, false
			}
		default:
			return nil, false
		}
	}
	return items, true
}
