// Package ui is the local workbench behind `lotsman ui` (feature 008, spec
// §4): a loopback HTTP server over internal/workbench and an embedded
// frontend. It writes one file, the configuration fixed at start, and is
// imported only by the ui command -- serve never sees it.
package ui
