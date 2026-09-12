// Package textnorm turns identifiers and paths into comparable words.
//
// It exists because two unrelated features need the same answer and must not
// disagree: the effect scanner asks "is `rebuild` one of the words in this
// operation's name?" and the search index asks "which words does this
// operation contain?". A tokenizer per caller would mean an operation whose
// effect was raised by a verb the index cannot find.
package textnorm
