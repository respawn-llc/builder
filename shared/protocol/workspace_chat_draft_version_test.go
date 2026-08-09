package protocol
import "testing"
func TestWorkspaceChatDraftRaisesProtocolVersion(t *testing.T) {
	if Version != "98" { t.Fatalf("version = %q", Version) }
}
