// Package openapi is the libopenapi adapter: parse, resolve $ref, normalize
// into the domain IR (pipeline stages 1-3).
//
// Constitution VIII: libopenapi types MUST NOT leave this package; everything
// downstream depends on internal/domain only.
package openapi
