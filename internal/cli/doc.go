// Package cli is the lotsman command line: the subcommands, their flags and
// their output. cmd/lotsman is only the process around it -- signals and the
// exit code -- so that this package can be driven, and read, without them.
//
// It is wiring only: flag parsing, logger construction and dependency
// assembly by hand (Constitution VII — no DI framework).
package cli
