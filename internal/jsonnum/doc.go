// Package jsonnum carries JSON numbers as the literals they were written as.
//
// A float64 holds integers exactly only up to 2^53, and decimals almost never:
// an id of 9007199254740993 or a bound of 9223372036854775807 becomes a
// neighbouring number the moment it is decoded into one, without an error. The
// literal is the value, so lotsman keeps it -- in arguments, in schema bounds
// -- and this package is what it may safely do with one.
package jsonnum
