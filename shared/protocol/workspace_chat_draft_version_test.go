package protocol
import "testing"
func TestWorkspaceChatDraftRaisesProtocolVersion(t *testing.T) {
	if Version != "102" { t.Fatalf("version = %q", Version) }
}
