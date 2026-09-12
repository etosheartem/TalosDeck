package executionauthority

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

// Identity pins the independent host and namespace; epochs are deliberately excluded.
func Identity(o SSHOptions) string {
	raw, _ := json.Marshal([]string{o.Host, o.RemoteBinary, o.StateDir})
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
