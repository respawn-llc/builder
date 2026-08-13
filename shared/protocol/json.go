package protocol

import "core/shared/jsoncontract"

// DecodeStrictJSON decodes exactly one JSON value and rejects unknown fields.
func DecodeStrictJSON(data []byte, target any) error {
	return jsoncontract.DecodeStrict(data, target)
}
