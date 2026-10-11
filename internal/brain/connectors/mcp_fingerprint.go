package connectors

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
)

// fpTool is one remote tool as the fingerprint sees it: the raw
// name, description and input schema the server listed.
type fpTool struct {
	name   string
	desc   string
	schema json.RawMessage
}

// fingerprintTools hashes a server's tool list so a reviewed list can
// be told apart from a changed one (D-157, issue #1205). Order and
// schema whitespace do not matter; any name, description or schema
// change does, since each is something the model acts on.
func fingerprintTools(ts []fpTool) string {
	sorted := make([]fpTool, len(ts))
	copy(sorted, ts)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].name < sorted[j].name })
	h := sha256.New()
	for _, t := range sorted {
		var schema bytes.Buffer
		if err := json.Compact(&schema, t.schema); err != nil {
			schema.Reset()
			schema.Write(t.schema)
		}
		h.Write([]byte(t.name))
		h.Write([]byte{0})
		h.Write([]byte(t.desc))
		h.Write([]byte{0})
		h.Write(schema.Bytes())
		h.Write([]byte{'\n'})
	}
	return hex.EncodeToString(h.Sum(nil))
}
