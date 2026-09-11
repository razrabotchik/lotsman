package buildinfo

import (
	"fmt"
	"runtime"
	"runtime/debug"
	"sync"
)

// Name is the binary and MCP server name reported to clients.
const Name = "lotsman"

// MCPProtocolVersion is the MCP revision this build targets. The SDK negotiates
// the effective revision per session and still speaks older ones; this constant
// is what we report in `lotsman version` and in ADR field notes.
const MCPProtocolVersion = "2026-07-28"

const mcpSDKModule = "github.com/modelcontextprotocol/go-sdk"

// Values injected at link time by the Makefile / goreleaser
// (-X github.com/razrabotchik/lotsman/internal/buildinfo.version=...).
var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

// Info is the build and protocol identity of the running binary.
type Info struct {
	Name               string `json:"name"`
	Version            string `json:"version"`
	Commit             string `json:"commit"`
	Date               string `json:"date"`
	GoVersion          string `json:"goVersion"`
	Platform           string `json:"platform"`
	MCPSDKVersion      string `json:"mcpSdkVersion"`
	MCPProtocolVersion string `json:"mcpProtocolVersion"`
}

var get = sync.OnceValue(func() Info {
	return Info{
		Name:               Name,
		Version:            version,
		Commit:             commit,
		Date:               date,
		GoVersion:          runtime.Version(),
		Platform:           runtime.GOOS + "/" + runtime.GOARCH,
		MCPSDKVersion:      moduleVersion(mcpSDKModule),
		MCPProtocolVersion: MCPProtocolVersion,
	}
})

// Get returns the build identity. It never fails: unknown values are reported
// as "unknown" rather than omitted, so field notes stay unambiguous.
func Get() Info { return get() }

// Version returns the binary version alone, for the MCP Implementation record.
func Version() string { return get().Version }

// String renders the identity as the `lotsman version` output, one field per line.
func (i Info) String() string {
	return fmt.Sprintf("%s %s\n"+
		"commit:       %s\n"+
		"built:        %s\n"+
		"go:           %s\n"+
		"platform:     %s\n"+
		"mcp sdk:      %s\n"+
		"mcp protocol: %s",
		i.Name, i.Version, i.Commit, i.Date, i.GoVersion, i.Platform,
		i.MCPSDKVersion, i.MCPProtocolVersion)
}

// moduleVersion reports the version of a dependency baked into this binary.
// `go run` and tests produce module info too; only a stripped or non-module
// build yields "unknown".
func moduleVersion(path string) string {
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return "unknown"
	}
	for _, dep := range bi.Deps {
		if dep.Path != path {
			continue
		}
		if dep.Replace != nil {
			return dep.Replace.Version
		}
		return dep.Version
	}
	return "unknown"
}
