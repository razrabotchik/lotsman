package config

import (
	"testing"
	"time"

	"github.com/razrabotchik/lotsman/internal/errs"
)

// The server section of docs/spec.md 5.1 parses. It is worth asserting
// literally: strict decoding means a section lotsman does not know about is a
// startup error, so a documented example that does not parse is a document
// that lies.
func TestServerSectionParses(t *testing.T) {
	file, err := Parse([]byte(`apiVersion: lotsman.dev/v1alpha1
kind: Runtime
server:
  transport: http
  listen: 127.0.0.1:9090
  logLevel: debug
  allowedOrigins: [https://console.example.com]
  allowedHosts: [mcp.example.com:9090]
  drainTimeout: 30s
`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if file.Server.Transport != TransportHTTP {
		t.Errorf("transport = %q, want http", file.Server.Transport)
	}
	if file.Server.Listen != "127.0.0.1:9090" {
		t.Errorf("listen = %q", file.Server.Listen)
	}
	if len(file.Server.AllowedOrigins) != 1 || len(file.Server.AllowedHosts) != 1 {
		t.Errorf("origins/hosts = %+v / %+v", file.Server.AllowedOrigins, file.Server.AllowedHosts)
	}
}

// A transport this build does not serve is refused at load. Falling back to
// stdio would leave an operator who wrote `transport: http` to find out from
// the absence of a socket.
func TestUnknownTransportIsRefused(t *testing.T) {
	_, err := Parse([]byte(`apiVersion: lotsman.dev/v1alpha1
server:
  transport: websocket
`))
	if err == nil {
		t.Fatal("an unknown transport was accepted")
	}
	if errs.ClassOf(err) != errs.ClassUsage {
		t.Errorf("class = %s, want usage", errs.ClassOf(err))
	}
}

func TestDrainTimeoutMustBeADuration(t *testing.T) {
	for _, value := range []string{"soon", "-5s"} {
		t.Run(value, func(t *testing.T) {
			_, err := Parse([]byte("apiVersion: lotsman.dev/v1alpha1\nserver:\n  drainTimeout: \"" + value + "\"\n"))
			if err == nil {
				t.Fatalf("drainTimeout %q was accepted", value)
			}
			if errs.ClassOf(err) != errs.ClassUsage {
				t.Errorf("class = %s, want usage", errs.ClassOf(err))
			}
		})
	}
}

// Saying nothing must produce the transport that cannot be reached from off
// the machine (FR-70). This is the assertion that fails if a default ever
// drifts into opening a socket.
func TestServerDefaultsAreLocal(t *testing.T) {
	runtime := Resolve(nil, Environment{}, Overrides{})
	if runtime.Server.Transport != TransportStdio {
		t.Errorf("transport = %q, want stdio", runtime.Server.Transport)
	}
	if runtime.Server.Listen != DefaultListen {
		t.Errorf("listen = %q, want %q", runtime.Server.Listen, DefaultListen)
	}
	if runtime.Server.DrainTimeout != DefaultDrainTimeout {
		t.Errorf("drainTimeout = %s, want %s", runtime.Server.DrainTimeout, DefaultDrainTimeout)
	}
	if runtime.Server.AllowUnauthenticatedPublicBind {
		t.Error("the dangerous opt-in is on by default")
	}
}

// FR-62's order holds for the server section too: defaults < file <
// environment < flags.
func TestServerPrecedence(t *testing.T) {
	file := &File{Server: Server{
		Transport:    TransportHTTP,
		Listen:       "127.0.0.1:1111",
		LogLevel:     "warn",
		DrainTimeout: "1s",
	}}

	fromFile := Resolve(file, Environment{}, Overrides{})
	if fromFile.Server.Listen != "127.0.0.1:1111" || fromFile.Server.DrainTimeout != time.Second {
		t.Fatalf("the file layer was not applied: %+v", fromFile.Server)
	}
	if fromFile.Server.LogLevel != "warn" {
		t.Errorf("logLevel = %q, want the file's warn", fromFile.Server.LogLevel)
	}

	fromEnv := Resolve(file, Environment{LogLevel: "debug"}, Overrides{})
	if fromEnv.Server.LogLevel != "debug" {
		t.Errorf("logLevel = %q, want the environment's debug", fromEnv.Server.LogLevel)
	}

	listen := "127.0.0.1:2222"
	level := "error"
	drain := 45 * time.Second
	fromFlags := Resolve(file, Environment{LogLevel: "debug"}, Overrides{
		Listen:       &listen,
		LogLevel:     &level,
		DrainTimeout: &drain,
	})
	if fromFlags.Server.Listen != listen || fromFlags.Server.LogLevel != level || fromFlags.Server.DrainTimeout != drain {
		t.Errorf("the flag layer did not win: %+v", fromFlags.Server)
	}
	// A setting no flag mentioned keeps what the file said.
	if fromFlags.Server.Transport != TransportHTTP {
		t.Errorf("transport = %q; a flag nobody passed overwrote the file", fromFlags.Server.Transport)
	}
}

// FR-79 at load time. Each of these is a configuration that would otherwise
// produce an endpoint the operator believes is protected and is not.
func TestInboundAuthValidation(t *testing.T) {
	cases := []struct {
		name  string
		yaml  string
		class errs.Class
	}{
		{
			name:  "a mode nobody defines",
			yaml:  "server:\n  inboundAuth:\n    mode: mtls\n",
			class: errs.ClassUsage,
		},
		{
			name:  "static-bearer with no secret",
			yaml:  "server:\n  inboundAuth:\n    mode: static-bearer\n",
			class: errs.ClassUsage,
		},
		{
			name:  "a literal secret instead of a reference",
			yaml:  "server:\n  inboundAuth:\n    mode: static-bearer\n    tokenRef: sekrit\n",
			class: errs.ClassUsage,
		},
		{
			name:  "a secret that authenticates nothing",
			yaml:  "server:\n  inboundAuth:\n    mode: none\n    tokenRef: env:TOKEN\n",
			class: errs.ClassUsage,
		},
		{
			name:  "a trusted proxy that is a name",
			yaml:  "server:\n  inboundAuth:\n    mode: static-bearer\n    tokenRef: env:TOKEN\n    trustedProxies: [proxy.example.com]\n",
			class: errs.ClassUsage,
		},
		{
			// `oauth` is implemented (feature 005), so an oauth section with
			// nothing in it is an incomplete configuration rather than a
			// missing capability.
			name:  "oauth with nothing configured",
			yaml:  "server:\n  inboundAuth:\n    mode: oauth\n",
			class: errs.ClassUsage,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse([]byte("apiVersion: lotsman.dev/v1alpha1\n" + tc.yaml))
			if err == nil {
				t.Fatal("the configuration was accepted")
			}
			if got := errs.ClassOf(err); got != tc.class {
				t.Errorf("class = %s, want %s (%v)", got, tc.class, err)
			}
		})
	}
}

func TestInboundAuthAccepted(t *testing.T) {
	file, err := Parse([]byte(`apiVersion: lotsman.dev/v1alpha1
server:
  inboundAuth:
    mode: static-bearer
    tokenRef: env:LOTSMAN_INBOUND
    trustedProxies: ["10.0.0.0/8", "192.168.1.7"]
`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	runtime := Resolve(file, Environment{}, Overrides{})
	if runtime.Server.InboundAuth.Mode != InboundStaticBearer {
		t.Errorf("mode = %q", runtime.Server.InboundAuth.Mode)
	}
	if len(runtime.Server.InboundAuth.TrustedProxies) != 2 {
		t.Errorf("trustedProxies = %+v", runtime.Server.InboundAuth.TrustedProxies)
	}
	// Saying nothing still means nothing authenticates the endpoint, stated
	// rather than left empty.
	if bare := Resolve(nil, Environment{}, Overrides{}); bare.Server.InboundAuth.Mode != InboundNone {
		t.Errorf("default mode = %q, want none", bare.Server.InboundAuth.Mode)
	}
}

// FR-80–81 at load time. Nothing here is inferred: an issuer lotsman guessed
// at, or a resource derived from a bind address, would be a value the
// operator never checked against what their provider actually mints.
func TestInboundOAuthValidation(t *testing.T) {
	const valid = `apiVersion: lotsman.dev/v1alpha1
server:
  inboundAuth:
    mode: oauth
    oauth:
      issuer: https://issuer.example.com
      resource: https://mcp.example.com/
      jwksURI: https://issuer.example.com/jwks
`
	if _, err := Parse([]byte(valid)); err != nil {
		t.Fatalf("a complete oauth section was refused: %v", err)
	}

	cases := []struct{ name, oauth string }{
		{name: "no issuer", oauth: "resource: https://mcp.example.com/\n      jwksURI: https://i.example.com/jwks"},
		{name: "no resource", oauth: "issuer: https://i.example.com\n      jwksURI: https://i.example.com/jwks"},
		{name: "no key set", oauth: "issuer: https://i.example.com\n      resource: https://mcp.example.com/"},
		{
			name:  "an issuer over plain http",
			oauth: "issuer: http://i.example.com\n      resource: https://mcp.example.com/\n      jwksURI: https://i.example.com/jwks",
		},
		{
			name:  "a key set over plain http",
			oauth: "issuer: https://i.example.com\n      resource: https://mcp.example.com/\n      jwksURI: http://i.example.com/jwks",
		},
		{
			name:  "a resource that is not a URI",
			oauth: "issuer: https://i.example.com\n      resource: mcp\n      jwksURI: https://i.example.com/jwks",
		},
		{
			// A resource server that accepts an HMAC algorithm accepts a
			// token signed with the key it verifies with.
			name: "a symmetric algorithm",
			oauth: "issuer: https://i.example.com\n      resource: https://mcp.example.com/\n" +
				"      jwksURI: https://i.example.com/jwks\n      algorithms: [HS256]",
		},
		{
			name: "alg none, written out",
			oauth: "issuer: https://i.example.com\n      resource: https://mcp.example.com/\n" +
				"      jwksURI: https://i.example.com/jwks\n      algorithms: [none]",
		},
		{
			name: "a TTL that is not a duration",
			oauth: "issuer: https://i.example.com\n      resource: https://mcp.example.com/\n" +
				"      jwksURI: https://i.example.com/jwks\n      jwksTTL: soon",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			document := "apiVersion: lotsman.dev/v1alpha1\nserver:\n  inboundAuth:\n    mode: oauth\n    oauth:\n      " + tc.oauth + "\n"
			_, err := Parse([]byte(document))
			if err == nil {
				t.Fatal("the configuration was accepted")
			}
			if errs.ClassOf(err) != errs.ClassUsage {
				t.Errorf("class = %s, want usage", errs.ClassOf(err))
			}
		})
	}
}
