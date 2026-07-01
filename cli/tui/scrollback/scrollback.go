package scrollback

type NativeOngoingSurface interface {
	Steer(line string) error
	StreamMarkdownAssistantContent(ansi string) error
	FinishAssistantStreaming() error
	DiscardAssistantStreaming() error
	RenderLive(frame NativeLiveAreaFrame) error
	AssistantStreaming() bool
	AssistantStreamTailLines() []string
	FlushHoldoff() error
}
