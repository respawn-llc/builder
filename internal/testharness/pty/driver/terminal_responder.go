package driver

// terminalResponder implements the small, fixed terminal-device protocol
// surface needed by interactive clients during capability probing. It is a
// bounded byte-state machine, not a rendered-text matcher.
type terminalResponder struct {
	state terminalResponderState
	body  []byte
}

type terminalResponderState uint8

const (
	terminalResponderGround terminalResponderState = iota
	terminalResponderEscape
	terminalResponderCSI
	terminalResponderOSC
	terminalResponderOSCEscape
)

const maxTerminalQueryBytes = 32

func (r *terminalResponder) Feed(payload []byte) [][]byte {
	replies := make([][]byte, 0, 2)
	for _, value := range payload {
		switch r.state {
		case terminalResponderGround:
			if value == 0x1b {
				r.state = terminalResponderEscape
			}
		case terminalResponderEscape:
			switch value {
			case '[':
				r.body = r.body[:0]
				r.state = terminalResponderCSI
			case ']':
				r.body = r.body[:0]
				r.state = terminalResponderOSC
			default:
				r.state = terminalResponderGround
			}
		case terminalResponderCSI:
			if len(r.body) < maxTerminalQueryBytes {
				r.body = append(r.body, value)
			}
			if value >= 0x40 && value <= 0x7e {
				if value == 'n' && string(r.body) == "6n" {
					replies = append(replies, []byte("\x1b[1;1R"))
				}
				r.state = terminalResponderGround
			}
		case terminalResponderOSC:
			switch value {
			case 0x07:
				replies = r.finishOSC(replies)
			case 0x1b:
				r.state = terminalResponderOSCEscape
			default:
				if len(r.body) < maxTerminalQueryBytes {
					r.body = append(r.body, value)
				}
			}
		case terminalResponderOSCEscape:
			if value == '\\' {
				replies = r.finishOSC(replies)
			} else {
				if len(r.body) < maxTerminalQueryBytes {
					r.body = append(r.body, 0x1b, value)
				}
				r.state = terminalResponderOSC
			}
		}
	}
	return replies
}

func (r *terminalResponder) finishOSC(replies [][]byte) [][]byte {
	if string(r.body) == "11;?" {
		replies = append(replies, []byte("\x1b]11;rgb:0000/0000/0000\x1b\\"))
	}
	r.body = r.body[:0]
	r.state = terminalResponderGround
	return replies
}
