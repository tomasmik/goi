package textnorm

import (
	"strings"

	"golang.org/x/text/unicode/norm"
)

// Normalize returns the stable comparison form stored in normalized database fields.
// Changing this behavior requires migrating existing normalized values.
func Normalize(value string) string {
	return strings.ToLower(strings.TrimSpace(norm.NFKC.String(value)))
}
