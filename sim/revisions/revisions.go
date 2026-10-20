// Package revisions holds what the clouds' revisioned container services
// share: a revision template's identity by content, and the choice of the
// revision a request goes to by traffic weight. Cloud Run services and Azure
// Container Apps apps route through it.
package revisions

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"math/rand/v2"
)

// Signature identifies a revision template by its content: two templates
// whose JSON encodings are equal are one revision.
func Signature(template any) (string, error) {
	encoded, err := json.Marshal(template)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:]), nil
}

// Weighted is a revision's share of the traffic.
type Weighted[T any] struct {
	Revision T
	Weight   int
}

// Pick chooses a revision in proportion to its weight among those whose
// weight is positive, and reports false when none is.
func Pick[T any](candidates []Weighted[T]) (T, bool) {
	total := 0
	for _, c := range candidates {
		if c.Weight > 0 {
			total += c.Weight
		}
	}
	var none T
	if total == 0 {
		return none, false
	}
	n := rand.IntN(total) // #nosec G404 -- traffic splitting, not a secret.
	for _, c := range candidates {
		if c.Weight <= 0 {
			continue
		}
		if n -= c.Weight; n < 0 {
			return c.Revision, true
		}
	}
	return none, false
}
