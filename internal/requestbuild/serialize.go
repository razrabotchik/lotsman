package requestbuild

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/razrabotchik/lotsman/internal/domain"
	"github.com/razrabotchik/lotsman/internal/errs"
)

// Percent-encoding sets (RFC 3986).
//
// unreserved is the only set that never needs encoding. Everything else is
// encoded by default -- including "/" inside a path parameter value, which is
// what stops an argument from escaping its path segment (pitfall #9).
const unreserved = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-._~"

// reservedKept is the subset of RFC 3986 reserved characters left literal in
// a query value when the spec sets allowReserved (FR-26).
//
// "&", "=" and "#" are deliberately NOT in it, although RFC 3986 calls them
// reserved. Leaving those literal would let an argument append its own
// parameters to the query string; allowReserved exists so values like dates
// and paths survive intact, not so a model can restructure the request.
const reservedKept = ":/?[]@!$'()*+,;"

// expandPath substitutes every {placeholder} in the template with its path
// argument, percent-encoded. The result is an *escaped* path: callers must
// not run it through url.URL.Path, which would decode it and hand back the
// traversal this encoding prevents.
func expandPath(template string, params []domain.Parameter, args map[string]any) (string, error) {
	var out strings.Builder
	rest := template

	for {
		open := strings.Index(rest, "{")
		if open < 0 {
			out.WriteString(rest)
			break
		}
		closing := strings.Index(rest[open:], "}")
		if closing < 0 {
			return "", errs.Errorf(errs.ClassSpecInvalid,
				"requestbuild: path template %q has an unclosed placeholder", template)
		}
		name := rest[open+1 : open+closing]
		out.WriteString(rest[:open])

		p, ok := findParameter(params, domain.LocationPath, name)
		if !ok {
			return "", errs.Errorf(errs.ClassUnsupported,
				"requestbuild: path placeholder %q has no declared path parameter", name)
		}
		value, present := args[name]
		if !present {
			return "", errs.Errorf(errs.ClassUsage,
				"requestbuild: path parameter %q is required", name)
		}
		rendered, err := renderSimple(p, value)
		if err != nil {
			return "", err
		}
		out.WriteString(rendered)
		rest = rest[open+closing+1:]
	}

	return out.String(), nil
}

// renderSimple serializes a path parameter (style: simple). Arrays join with
// a comma at both explode settings -- explode only changes object rendering,
// and objects are refused before they reach here.
func renderSimple(p *domain.Parameter, value any) (string, error) {
	if list, ok := value.([]any); ok {
		parts := make([]string, 0, len(list))
		for _, item := range list {
			s, err := scalarString(p.Name, item)
			if err != nil {
				return "", err
			}
			parts = append(parts, percentEncode(s, false))
		}
		return strings.Join(parts, ","), nil
	}
	s, err := scalarString(p.Name, value)
	if err != nil {
		return "", err
	}
	return percentEncode(s, false), nil
}

// buildQuery serializes query parameters (style: form) in declaration order.
//
// It writes the query string by hand: url.Values.Encode sorts keys, always
// percent-encodes, and cannot express allowReserved or a non-exploded array,
// so it is the wrong tool for three separate reasons (FR-26).
func buildQuery(params []domain.Parameter, args map[string]any) (string, error) {
	var parts []string

	for i := range params {
		p := &params[i]
		if p.In != domain.LocationQuery {
			continue
		}
		value, present := args[p.Name]
		if !present {
			if p.Required {
				return "", errs.Errorf(errs.ClassUsage,
					"requestbuild: query parameter %q is required", p.Name)
			}
			continue
		}
		key := percentEncode(p.Name, false)

		list, isList := value.([]any)
		if !isList {
			rendered, err := renderQueryScalar(p, value)
			if err != nil {
				return "", err
			}
			parts = append(parts, key+"="+rendered)
			continue
		}
		if p.Explode {
			// ?ids=1&ids=2 -- the default, and the one converters get wrong.
			for _, item := range list {
				rendered, err := renderQueryScalar(p, item)
				if err != nil {
					return "", err
				}
				parts = append(parts, key+"="+rendered)
			}
			continue
		}
		// ?ids=1,2 -- the comma is the separator, so it stays literal while
		// any comma inside a value is encoded.
		rendered := make([]string, 0, len(list))
		for _, item := range list {
			s, err := renderQueryScalar(p, item)
			if err != nil {
				return "", err
			}
			rendered = append(rendered, s)
		}
		parts = append(parts, key+"="+strings.Join(rendered, ","))
	}

	return strings.Join(parts, "&"), nil
}

func renderQueryScalar(p *domain.Parameter, value any) (string, error) {
	s, err := scalarString(p.Name, value)
	if err != nil {
		return "", err
	}
	return percentEncode(s, p.AllowReserved), nil
}

// scalarString renders one JSON scalar the way the wire needs it. Arguments
// arrive from JSON, so every number is a float64 or json.Number: an integer
// must come back out as "42", not "42.0".
func scalarString(name string, value any) (string, error) {
	switch v := value.(type) {
	case string:
		return v, nil
	case bool:
		return strconv.FormatBool(v), nil
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64), nil
	case float32:
		return strconv.FormatFloat(float64(v), 'f', -1, 32), nil
	case int:
		return strconv.Itoa(v), nil
	case int64:
		return strconv.FormatInt(v, 10), nil
	case json.Number:
		return v.String(), nil
	case nil:
		// A URL has no representation for null. Sending an empty value
		// instead would be a guess about what the API means by it.
		return "", errs.Errorf(errs.ClassUsage,
			"requestbuild: parameter %q is null, which has no URL representation", name)
	default:
		return "", errs.Errorf(errs.ClassUsage,
			"requestbuild: parameter %q has value type %T, which is not a scalar", name, value)
	}
}

// percentEncode encodes s for use in a URL, keeping only unreserved
// characters (plus the reserved subset when allowReserved is set).
//
// It encodes byte by byte on the UTF-8 form, which is what RFC 3986 requires
// for non-ASCII text.
func percentEncode(s string, allowReserved bool) string {
	var out strings.Builder
	out.Grow(len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case strings.IndexByte(unreserved, c) >= 0:
			out.WriteByte(c)
		case allowReserved && strings.IndexByte(reservedKept, c) >= 0:
			out.WriteByte(c)
		default:
			out.WriteString(fmt.Sprintf("%%%02X", c))
		}
	}
	return out.String()
}

func findParameter(params []domain.Parameter, in domain.ParameterLocation, name string) (*domain.Parameter, bool) {
	for i := range params {
		if params[i].In == in && params[i].Name == name {
			return &params[i], true
		}
	}
	return nil, false
}

// applyHeaders writes header parameters (style: simple) onto the request.
//
// Header values are the one place where a value that "looks fine" can change
// the shape of the request rather than its content: a CR or LF splits the
// header block and injects headers of the attacker's choosing (pitfall #10).
// Nothing is stripped or escaped -- the value is refused, because a header
// lotsman had to rewrite is not the header the caller asked for.
func applyHeaders(req *http.Request, params []domain.Parameter, args map[string]any) error {
	for i := range params {
		p := &params[i]
		if p.In != domain.LocationHeader {
			continue
		}
		if domain.IsProtectedHeader(p.Name) {
			return errs.Errorf(errs.ClassPolicy,
				"requestbuild: header parameter %q controls the transport or lotsman's own credentials and is never settable", p.Name)
		}
		if !validHeaderName(p.Name) {
			return errs.Errorf(errs.ClassSpecInvalid,
				"requestbuild: header parameter %q is not a valid header name", p.Name)
		}

		value, present := args[p.Name]
		if !present {
			if p.Required {
				return errs.Errorf(errs.ClassUsage, "requestbuild: header parameter %q is required", p.Name)
			}
			continue
		}
		rendered, err := renderHeaderValue(p, value)
		if err != nil {
			return err
		}
		req.Header.Set(p.Name, rendered)
	}
	return nil
}

// renderHeaderValue serializes a header parameter: simple style, so an array
// joins with commas. Unlike a URL value it is not percent-encoded -- header
// values are not URL-encoded -- so every character is checked instead.
func renderHeaderValue(p *domain.Parameter, value any) (string, error) {
	var parts []string
	if list, ok := value.([]any); ok {
		for _, item := range list {
			s, err := scalarString(p.Name, item)
			if err != nil {
				return "", err
			}
			parts = append(parts, s)
		}
	} else {
		s, err := scalarString(p.Name, value)
		if err != nil {
			return "", err
		}
		parts = append(parts, s)
	}

	rendered := strings.Join(parts, ",")
	if !validHeaderValue(rendered) {
		return "", errs.Errorf(errs.ClassUsage,
			"requestbuild: header parameter %q contains a character that is not allowed in a header value", p.Name)
	}
	return rendered, nil
}

// validHeaderName accepts an RFC 9110 token.
func validHeaderName(name string) bool {
	if name == "" {
		return false
	}
	// The punctuation RFC 9110 allows in a token, alongside ALPHA/DIGIT.
	const tokenPunctuation = "!#$%&'*+-.^_`|~" //nolint:gosec // G101: this is the tchar set, not a credential
	for i := 0; i < len(name); i++ {
		c := name[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case strings.IndexByte(tokenPunctuation, c) >= 0:
		default:
			return false
		}
	}
	return true
}

// validHeaderValue accepts printable US-ASCII only. That is stricter than RFC
// 9110 (which tolerates obs-text and horizontal tab); the stricter rule costs
// nothing real and leaves no room for a smuggled control character.
func validHeaderValue(value string) bool {
	for i := 0; i < len(value); i++ {
		if value[i] < 0x20 || value[i] > 0x7E {
			return false
		}
	}
	return true
}
