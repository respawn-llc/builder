package tools

type EditInput struct {
	Path       string `json:"path" jsonschema_description:"File path to edit. Relative paths resolve from the workspace root; absolute paths are allowed."`
	OldString  string `json:"old_string" jsonschema_description:"Exact current text to replace. Include enough surrounding context to make the match unique. Use an empty string only to write from a missing or empty file."`
	NewString  string `json:"new_string" jsonschema_description:"Replacement text. Use an empty string to delete the matched text."`
	ReplaceAll bool   `json:"replace_all,omitempty" jsonschema_description:"Replace all occurrences of the selected match. Defaults to false."`
}
