package main

import (
	"encoding/json"
	"io"
)

// newJSONEncoder returns the encoder used for every machine-readable output:
// indented, HTML escaping off (report text is not HTML, and escaping mangles
// URLs from the spec).
func newJSONEncoder(w io.Writer) *json.Encoder {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	return enc
}
