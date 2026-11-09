package textnorm

import (
	"strings"

	"github.com/tomasmik/goi/internal/kana"
	"golang.org/x/text/unicode/norm"
)

// Normalize returns the stable comparison form stored in normalized database fields.
// Changing this behavior requires migrating existing normalized values.
func Normalize(value string) string {
	return strings.ToLower(strings.TrimSpace(norm.NFKC.String(value)))
}

func Reading(value string) string {
	return kana.ToHiragana(Normalize(value))
}
