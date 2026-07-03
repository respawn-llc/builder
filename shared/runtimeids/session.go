package runtimeids

type SessionID struct{ uuidv4Value }

func ParseSessionID(raw string) (SessionID, error) {
	id, err := parseUUIDv4Value(raw, "session_id")
	return SessionID{uuidv4Value: id}, err
}
