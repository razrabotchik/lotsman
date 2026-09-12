package textnorm

import (
	"strings"
	"unicode"
)

// Tokens splits an identifier or path into lower-case words, breaking on
// non-alphanumeric characters and camelCase boundaries, so that
// "rebuildCache", "rebuild_cache" and "/rebuild-cache" all yield
// ["rebuild", "cache"].
//
// Acronyms are kept whole up to the last capital that starts a new word:
// "getAPIKey" is ["get", "api", "key"], not ["get", "a", "p", "i", "key"].
func Tokens(s string) []string {
	var tokens []string
	var current strings.Builder

	flush := func() {
		if current.Len() > 0 {
			tokens = append(tokens, strings.ToLower(current.String()))
			current.Reset()
		}
	}

	runes := []rune(s)
	for i, r := range runes {
		switch {
		case unicode.IsUpper(r):
			// A camelCase boundary, but not inside an acronym: "APIKey"
			// breaks before "Key", not between "A" and "P".
			if i > 0 && (unicode.IsLower(runes[i-1]) || unicode.IsDigit(runes[i-1]) ||
				(i+1 < len(runes) && unicode.IsLower(runes[i+1]))) {
				flush()
			}
			current.WriteRune(r)
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			current.WriteRune(r)
		default:
			flush()
		}
	}
	flush()
	return tokens
}

// Contains reports whether s contains word as a whole token. Substring
// matching is what makes "/updates" look like the verb "update", so it is not
// available here at all.
func Contains(s, word string) bool {
	for _, token := range Tokens(s) {
		if token == word {
			return true
		}
	}
	return false
}
