package clientui

type ProcessOutputChunk struct {
	ProcessID       string
	OffsetBytes     int64
	NextOffsetBytes int64
	Text            string
}
