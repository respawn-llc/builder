package patch

import patchformat "core/shared/transcript/patchformat"

const hunkMaxFuzz = 8

type editHunk struct {
	header    hunkHeader
	changes   []patchformat.ChangeLine
	endOfFile bool
}

type hunkHeader struct {
	hasPosition bool
	context     string
	oldStart    int
	oldCount    int
	newStart    int
	newCount    int
}
