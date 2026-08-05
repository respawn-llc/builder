package runtimeids

import "encoding/json"

// ReviewerFeedbackID identifies one persisted Reviewer feedback row.
type ReviewerFeedbackID struct{ uuidv4Value }

func ParseReviewerFeedbackID(raw string) (ReviewerFeedbackID, error) {
	id, err := parseCanonicalReviewerID(raw, "reviewer_feedback_id")
	return ReviewerFeedbackID{uuidv4Value: id}, err
}

func NewReviewerFeedbackID() ReviewerFeedbackID {
	return ReviewerFeedbackID{uuidv4Value: newUUIDv4Value()}
}

func (id *ReviewerFeedbackID) UnmarshalJSON(data []byte) error {
	var raw string
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	parsed, err := ParseReviewerFeedbackID(raw)
	if err != nil {
		return err
	}
	*id = parsed
	return nil
}

// ReviewerErrorID identifies one persisted Reviewer generation error row.
type ReviewerErrorID struct{ uuidv4Value }

func ParseReviewerErrorID(raw string) (ReviewerErrorID, error) {
	id, err := parseCanonicalReviewerID(raw, "reviewer_error_id")
	return ReviewerErrorID{uuidv4Value: id}, err
}

func parseCanonicalReviewerID(raw string, field string) (uuidv4Value, error) {
	parsed, err := ParseCanonicalUUIDv4(raw, field)
	if err != nil {
		return uuidv4Value{}, err
	}
	return uuidv4Value{value: parsed}, nil
}

func NewReviewerErrorID() ReviewerErrorID {
	return ReviewerErrorID{uuidv4Value: newUUIDv4Value()}
}

func (id *ReviewerErrorID) UnmarshalJSON(data []byte) error {
	var raw string
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	parsed, err := ParseReviewerErrorID(raw)
	if err != nil {
		return err
	}
	*id = parsed
	return nil
}
