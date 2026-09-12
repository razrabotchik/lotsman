// Package argvalidate checks tool arguments against the published input
// schema before anything is serialized (pipeline stage 5).
//
// This is the boundary where an argument invented by a model is rejected
// rather than silently dropped or smuggled into a URL, so it validates
// against exactly the schema the client was given -- not a reconstruction of
// it (FR-23).
package argvalidate
