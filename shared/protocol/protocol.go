package protocol

import (
	_ "embed"
	"encoding/json"
	"strings"
)

//go:embed version.json
var versionDefinition []byte

var Version = mustLoadVersion()

const (
	RPCPath        = "/rpc"
	HealthPath     = "/healthz"
	HealthStatusOK = "ok"
	ReadinessPath  = "/readyz"
)

type ServerIdentity struct {
	ProtocolVersion string `json:"protocol_version"`
	ServerID        string `json:"server_id"`
	PID             int    `json:"pid"`
	// PersistenceRootID is a short, stable hash of the server's persistence
	// root (see config.PersistenceRootHash). Clients that explicitly select a
	// non-default root use it to confirm an attached server serves that root
	// instead of a different instance reachable on the same TCP endpoint. A
	// client that did not select an explicit root ignores this field entirely;
	// a client that did select one rejects any server that does not report a
	// matching id (including an empty id from an older build).
	PersistenceRootID string `json:"persistence_root_id,omitempty"`
}

func mustLoadVersion() string {
	var definition struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(versionDefinition, &definition); err != nil {
		panic("load protocol version: " + err.Error())
	}
	version := strings.TrimSpace(definition.Version)
	if version == "" {
		panic("load protocol version: version is required")
	}
	return version
}
