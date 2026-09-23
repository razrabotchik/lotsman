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
