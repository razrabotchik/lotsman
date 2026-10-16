// Package workbench is the engine behind `lotsman ui` and `inspect
// --diff-against` (feature 008, spec §3): it keeps a draft of the
// configuration, runs the same pipeline serve runs over it, and says what a
// change fixes, breaks or alters before anything is written.
//
// It has no HTTP and does not know OpenAPI: the pipeline is handed to it as
// functions, so that what the workbench predicts is what serve does.
package workbench
