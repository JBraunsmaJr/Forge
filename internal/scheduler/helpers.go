package scheduler

import (
	"crypto/rand"
	"fmt"
)

// newID generates a random 32-character hex ID.
// Used for run IDs, job IDs, lease tokens, and session IDs.
func newID() string {
	b := make([]byte, 16)
	rand.Read(b)
	return fmt.Sprintf("%x", b)
}
