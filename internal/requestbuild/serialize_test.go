package requestbuild

import (
	"io"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/razrabotchik/lotsman/internal/domain"
)

const testBase = "https://api.example.com"

func pathParam(name string) domain.Parameter {
	return domain.Parameter{
		Name: name, In: domain.LocationPath, Required: true,
		Style: domain.StyleSimple, Explode: false,
		Schema: domain.Schema{"type": "string"},
	}
}

func queryParam(name string, explode bool) domain.Parameter {
	return domain.Parameter{
		Name: name, In: domain.LocationQuery,
		Style: domain.StyleForm, Explode: explode,
		Schema: domain.Schema{"type": "string"},
	}
}

func buildURL(t *testing.T, op *Operation, args Arguments) string {
	t.Helper()
	op.Method = "GET"
	op.Servers = []string{testBase}
	req, err := Build(t.Context(), op, args, Options{})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	return req.URL.String()
}

// The serialization table: every row is a documented OAS rule, and the two
// rows marked as pitfalls are the ones conversions get wrong most often.
func TestSerializationTable(t *testing.T) {
	tests := []struct {
		name   string
		params []domain.Parameter
		args   Arguments
		want   string
	}{
		{
			name:   "path simple scalar",
			params: []domain.Parameter{pathParam("widgetId")},
			args:   Arguments{"path": map[string]any{"widgetId": "w-1"}},
			want:   testBase + "/widgets/w-1",
		},
		{
			name:   "path simple array joins with commas",
			params: []domain.Parameter{pathParam("widgetId")},
			args:   Arguments{"path": map[string]any{"widgetId": []any{"a", "b", "c"}}},
			want:   testBase + "/widgets/a,b,c",
		},
		{
			// Pitfall #9: a path argument must stay inside its own segment.
			name:   "path traversal is encoded, not honoured",
			params: []domain.Parameter{pathParam("widgetId")},
			args:   Arguments{"path": map[string]any{"widgetId": "../../etc/passwd"}},
			want:   testBase + "/widgets/..%2F..%2Fetc%2Fpasswd",
		},
		{
			name:   "path reserved characters are encoded",
			params: []domain.Parameter{pathParam("widgetId")},
			args:   Arguments{"path": map[string]any{"widgetId": "a b?c#d&e=f"}},
			want:   testBase + "/widgets/a%20b%3Fc%23d%26e%3Df",
		},
		{
			// Pitfall #5: a query array defaults to explode=true.
			name:   "query array exploded",
			params: []domain.Parameter{queryParam("ids", true)},
			args:   Arguments{"query": map[string]any{"ids": []any{"1", "2"}}},
			want:   testBase + "/widgets?ids=1&ids=2",
		},
		{
			name:   "query array not exploded",
			params: []domain.Parameter{queryParam("ids", false)},
			args:   Arguments{"query": map[string]any{"ids": []any{"1", "2"}}},
			want:   testBase + "/widgets?ids=1,2",
		},
		{
			name:   "query array not exploded encodes commas inside values",
			params: []domain.Parameter{queryParam("ids", false)},
			args:   Arguments{"query": map[string]any{"ids": []any{"a,b", "c"}}},
			want:   testBase + "/widgets?ids=a%2Cb,c",
		},
		{
			name:   "query keeps declaration order, not alphabetical",
			params: []domain.Parameter{queryParam("zebra", true), queryParam("alpha", true)},
			args:   Arguments{"query": map[string]any{"alpha": "1", "zebra": "2"}},
			want:   testBase + "/widgets?zebra=2&alpha=1",
		},
		{
			name:   "query omits absent optional parameters",
			params: []domain.Parameter{queryParam("limit", true), queryParam("cursor", true)},
			args:   Arguments{"query": map[string]any{"limit": "10"}},
			want:   testBase + "/widgets?limit=10",
		},
		{
			name:   "query space encodes as %20, never +",
			params: []domain.Parameter{queryParam("q", true)},
			args:   Arguments{"query": map[string]any{"q": "two words"}},
			want:   testBase + "/widgets?q=two%20words",
		},
		{
			name:   "query encodes non-ASCII as UTF-8 bytes",
			params: []domain.Parameter{queryParam("q", true)},
			args:   Arguments{"query": map[string]any{"q": "щука"}},
			want:   testBase + "/widgets?q=%D1%89%D1%83%D0%BA%D0%B0",
		},
		{
			name: "allowReserved keeps path-ish characters literal",
			params: []domain.Parameter{{
				Name: "prefix", In: domain.LocationQuery, Style: domain.StyleForm,
				Explode: true, AllowReserved: true, Schema: domain.Schema{"type": "string"},
			}},
			args: Arguments{"query": map[string]any{"prefix": "a/b:c"}},
			want: testBase + "/widgets?prefix=a/b:c",
		},
		{
			// allowReserved must not let an argument restructure the query.
			name: "allowReserved still encodes query separators",
			params: []domain.Parameter{{
				Name: "prefix", In: domain.LocationQuery, Style: domain.StyleForm,
				Explode: true, AllowReserved: true, Schema: domain.Schema{"type": "string"},
			}},
			args: Arguments{"query": map[string]any{"prefix": "x&admin=true#frag"}},
			want: testBase + "/widgets?prefix=x%26admin%3Dtrue%23frag",
		},
		{
			name:   "integers do not arrive back as floats",
			params: []domain.Parameter{queryParam("limit", true)},
			args:   Arguments{"query": map[string]any{"limit": float64(42)}},
			want:   testBase + "/widgets?limit=42",
		},
		{
			name:   "non-integral numbers keep their fraction",
			params: []domain.Parameter{queryParam("ratio", true)},
			args:   Arguments{"query": map[string]any{"ratio": 1.5}},
			want:   testBase + "/widgets?ratio=1.5",
		},
		{
			name:   "booleans render as true/false",
			params: []domain.Parameter{queryParam("deep", true)},
			args:   Arguments{"query": map[string]any{"deep": true}},
			want:   testBase + "/widgets?deep=true",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			template := "/widgets"
			for _, p := range tt.params {
				if p.In == domain.LocationPath {
					template = "/widgets/{" + p.Name + "}"
				}
			}
			got := buildURL(t, &Operation{PathTemplate: template, Parameters: tt.params}, tt.args)
			if got != tt.want {
				t.Errorf("URL = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestSerializationRefusals(t *testing.T) {
	tests := []struct {
		name   string
		op     Operation
		args   Arguments
		reason string
	}{
		{
			name:   "missing required path argument",
			op:     Operation{PathTemplate: "/widgets/{widgetId}", Parameters: []domain.Parameter{pathParam("widgetId")}},
			args:   Arguments{},
			reason: "required",
		},
		{
			name: "missing required query argument",
			op: Operation{PathTemplate: "/widgets", Parameters: []domain.Parameter{{
				Name: "tenant", In: domain.LocationQuery, Required: true,
				Style: domain.StyleForm, Explode: true, Schema: domain.Schema{"type": "string"},
			}}},
			args:   Arguments{},
			reason: "required",
		},
		{
			name:   "placeholder without a declared parameter",
			op:     Operation{PathTemplate: "/widgets/{widgetId}"},
			args:   Arguments{"path": map[string]any{"widgetId": "w-1"}},
			reason: "no declared path parameter",
		},
		{
			name:   "null has no URL representation",
			op:     Operation{PathTemplate: "/widgets", Parameters: []domain.Parameter{queryParam("q", true)}},
			args:   Arguments{"query": map[string]any{"q": nil}},
			reason: "null",
		},
		{
			name:   "objects are not scalars",
			op:     Operation{PathTemplate: "/widgets", Parameters: []domain.Parameter{queryParam("q", true)}},
			args:   Arguments{"query": map[string]any{"q": map[string]any{"a": 1}}},
			reason: "not a scalar",
		},

		{
			name: "cookie parameters are refused, not dropped",
			op: Operation{PathTemplate: "/widgets", Parameters: []domain.Parameter{{
				Name: "session", In: domain.LocationCookie, Style: domain.StyleForm,
				Explode: true, Schema: domain.Schema{"type": "string"},
			}}},
			args:   Arguments{"cookies": map[string]any{"session": "abc"}},
			reason: "not serialized yet",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.op.Method = "GET"
			tt.op.Servers = []string{testBase}
			_, err := Build(t.Context(), &tt.op, tt.args, Options{})
			if err == nil {
				t.Fatal("want a refusal before the network")
			}
			if !strings.Contains(err.Error(), tt.reason) {
				t.Errorf("error = %q, want it to mention %q", err, tt.reason)
			}
		})
	}
}

// FuzzParameterEncoding pins the two invariants that matter for any value a
// model can send: the value survives a round trip, and it cannot escape the
// syntactic slot it was put in.
func FuzzParameterEncoding(f *testing.F) {
	for _, seed := range []string{
		"plain", "../../etc/passwd", "a/b", "a?b#c", "a&b=c", "a b", "щука",
		"%2e%2e%2f", "x\r\nInjected: 1", "'; DROP TABLE--", "\x00\x01", "a,b", "+", "%", "{id}",
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, value string) {
		op := Operation{
			Method:       "GET",
			PathTemplate: "/widgets/{widgetId}",
			Servers:      []string{testBase},
			Parameters:   []domain.Parameter{pathParam("widgetId"), queryParam("q", true)},
		}
		req, err := Build(t.Context(), &op, Arguments{
			"path":  map[string]any{"widgetId": value},
			"query": map[string]any{"q": value},
		}, Options{})
		if err != nil {
			t.Fatalf("Build(%q): %v", value, err)
		}

		// The path argument occupies exactly one segment, whatever it contains.
		segments := strings.Split(strings.TrimPrefix(req.URL.EscapedPath(), "/"), "/")
		if len(segments) != 2 || segments[0] != "widgets" {
			t.Fatalf("value %q escaped its path segment: %q", value, req.URL.EscapedPath())
		}
		decoded, err := url.PathUnescape(segments[1])
		if err != nil {
			t.Fatalf("value %q produced an unparseable path: %v", value, err)
		}
		if decoded != value {
			t.Fatalf("path round trip: got %q, want %q", decoded, value)
		}

		// The query argument stays one parameter with one value.
		values := req.URL.Query()
		if got := values["q"]; len(got) != 1 || got[0] != value {
			t.Fatalf("query round trip: got %#v, want exactly [%q]", got, value)
		}
		if strings.ContainsAny(req.URL.RawQuery[:len("q=")], "\r\n") {
			t.Fatalf("value %q injected a newline into the query", value)
		}
	})
}

func headerParam(name string) domain.Parameter {
	return domain.Parameter{
		Name: name, In: domain.LocationHeader,
		Style: domain.StyleSimple, Schema: domain.Schema{"type": "string"},
	}
}

func TestHeaderSerialization(t *testing.T) {
	op := &Operation{
		Method: "GET", PathTemplate: "/widgets", Servers: []string{testBase},
		Parameters: []domain.Parameter{headerParam("X-Tenant"), headerParam("X-Tags")},
	}
	req, err := Build(t.Context(), op, Arguments{"headers": map[string]any{
		"X-Tenant": "acme",
		"X-Tags":   []any{"a", "b"},
	}}, Options{})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if got := req.Header.Get("X-Tenant"); got != "acme" {
		t.Errorf("X-Tenant = %q, want acme", got)
	}
	if got := req.Header.Get("X-Tags"); got != "a,b" {
		t.Errorf("X-Tags = %q, want a,b (simple style joins with commas)", got)
	}
}

// Pitfall #10: a CR or LF in a header value splits the header block. The
// value is refused, never sanitized -- a header lotsman had to rewrite is not
// the header the caller asked for.
func TestHeaderValueInjectionIsRefused(t *testing.T) {
	for _, value := range []string{
		"acme\r\nX-Admin: true",
		"acme\nX-Admin: true",
		"acme\rX-Admin: true",
		"acme\x00",
		"acme\x7f",
		"acme\tvalue",
	} {
		t.Run(strconv.Quote(value), func(t *testing.T) {
			op := &Operation{
				Method: "GET", PathTemplate: "/widgets", Servers: []string{testBase},
				Parameters: []domain.Parameter{headerParam("X-Tenant")},
			}
			_, err := Build(t.Context(), op, Arguments{"headers": map[string]any{"X-Tenant": value}}, Options{})
			if err == nil {
				t.Fatalf("Build accepted a header value containing %q", value)
			}
			if !strings.Contains(err.Error(), "not allowed in a header value") {
				t.Errorf("error = %q, want it to name the offending value's character class", err)
			}
		})
	}
}

// An array element must be checked too, not just a scalar value.
func TestHeaderArrayElementInjectionIsRefused(t *testing.T) {
	op := &Operation{
		Method: "GET", PathTemplate: "/widgets", Servers: []string{testBase},
		Parameters: []domain.Parameter{headerParam("X-Tags")},
	}
	_, err := Build(t.Context(), op, Arguments{"headers": map[string]any{
		"X-Tags": []any{"ok", "bad\r\nX-Admin: true"},
	}}, Options{})
	if err == nil {
		t.Fatal("Build accepted an injected array element")
	}
}

func TestForbiddenHeaderParametersAreRefused(t *testing.T) {
	for _, name := range []string{"Authorization", "authorization", "Host", "Content-Length", "Cookie", "Content-Type", "Transfer-Encoding"} {
		t.Run(name, func(t *testing.T) {
			if !domain.IsProtectedHeader(name) {
				t.Fatalf("IsProtectedHeader(%q) = false", name)
			}
			op := &Operation{
				Method: "GET", PathTemplate: "/widgets", Servers: []string{testBase},
				Parameters: []domain.Parameter{headerParam(name)},
			}
			_, err := Build(t.Context(), op, Arguments{"headers": map[string]any{name: "x"}}, Options{})
			if err == nil {
				t.Fatalf("Build let a spec set %s", name)
			}
		})
	}
}

func TestJSONBodyIsSent(t *testing.T) {
	op := &Operation{
		Method: "POST", PathTemplate: "/widgets", Servers: []string{testBase},
		Body: &domain.BodySpec{
			MediaType: "application/json", Required: true,
			Schema: domain.Schema{"type": "object"},
		},
	}
	req, err := Build(t.Context(), op, Arguments{"body": map[string]any{"name": "Murka", "tags": []any{"cat"}}}, Options{})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if got := req.Header.Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q", got)
	}
	body, err := io.ReadAll(req.Body)
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"name":"Murka","tags":["cat"]}`; string(body) != want {
		t.Errorf("body = %s, want %s", body, want)
	}
	if req.ContentLength != int64(len(body)) {
		t.Errorf("ContentLength = %d, want %d", req.ContentLength, len(body))
	}
}

func TestBodyRefusals(t *testing.T) {
	jsonBody := &domain.BodySpec{MediaType: "application/json", Required: true, Schema: domain.Schema{"type": "object"}}
	optional := &domain.BodySpec{MediaType: "application/json", Schema: domain.Schema{"type": "object"}}

	t.Run("required body missing", func(t *testing.T) {
		op := &Operation{Method: "POST", PathTemplate: "/widgets", Servers: []string{testBase}, Body: jsonBody}
		if _, err := Build(t.Context(), op, Arguments{}, Options{}); err == nil {
			t.Fatal("Build sent a request without the required body")
		}
	})

	t.Run("body for an operation that declares none", func(t *testing.T) {
		op := &Operation{Method: "GET", PathTemplate: "/widgets", Servers: []string{testBase}}
		if _, err := Build(t.Context(), op, Arguments{"body": map[string]any{"x": 1}}, Options{}); err == nil {
			t.Fatal("Build invented a request body")
		}
	})

	t.Run("optional body omitted", func(t *testing.T) {
		op := &Operation{Method: "POST", PathTemplate: "/widgets", Servers: []string{testBase}, Body: optional}
		req, err := Build(t.Context(), op, Arguments{}, Options{})
		if err != nil {
			t.Fatalf("Build: %v", err)
		}
		if req.Header.Get("Content-Type") != "" {
			t.Error("a request with no body must not claim a content type")
		}
	})
}
