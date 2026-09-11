// Package specsource reads an OpenAPI document from an untrusted source
// (file or stdin).
//
// Pipeline stage 0: byte and time limits, YAML alias budget, sha256 digest.
// No parsing happens here.
package specsource
