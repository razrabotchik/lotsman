module github.com/razrabotchik/lotsman

// Go floor 1.25: the highest floor among dependencies (libopenapi v0.38.7 asks
// for 1.25.7; the MCP go-sdk asks for 1.25.0) — Constitution, Constraints.
// This is a compatibility claim, not the toolchain CI builds with: that one
// is pinned in the workflows, because an end-of-life line stops taking
// security fixes while this number keeps looking fine.
go 1.25.7

require (
	github.com/golang-jwt/jwt/v5 v5.3.1
	github.com/modelcontextprotocol/go-sdk v1.7.0
	github.com/pb33f/libopenapi v0.38.7
	github.com/santhosh-tekuri/jsonschema/v6 v6.0.2
	go.yaml.in/yaml/v4 v4.0.0-rc.6
)

require (
	github.com/bahlo/generic-list-go v0.2.0 // indirect
	github.com/buger/jsonparser v1.1.2 // indirect
	github.com/google/jsonschema-go v0.4.3 // indirect
	github.com/pb33f/jsonpath v0.8.2 // indirect
	github.com/pb33f/ordered-map/v2 v2.3.1 // indirect
	github.com/segmentio/asm v1.1.3 // indirect
	github.com/segmentio/encoding v0.5.4 // indirect
	github.com/yosida95/uritemplate/v3 v3.0.2 // indirect
	golang.org/x/oauth2 v0.35.0 // indirect
	golang.org/x/sync v0.22.0 // indirect
	golang.org/x/sys v0.41.0 // indirect
	golang.org/x/text v0.14.0 // indirect
	golang.org/x/time v0.15.0 // indirect
)
