package searchindex

import (
	"math"
	"sort"
	"strings"

	"github.com/razrabotchik/lotsman/internal/domain"
	"github.com/razrabotchik/lotsman/internal/textnorm"
)

// BM25 parameters. These are the literature defaults, deliberately untuned:
// FR-54 asks for a benchmark before any ranking claim, and a constant chosen to
// make one corpus look good is a claim.
const (
	k1 = 1.2
	b  = 0.75
)

// Field weights. A term in an operation's name says more about what the
// operation *is* than the same term in its prose: names and paths are the
// API's own vocabulary, and a summary is where a writer repeats the module
// name in every sentence.
//
// They are term-frequency multipliers, which is BM25F in its simple form.
const (
	weightName    = 3
	weightPath    = 2
	weightTag     = 2
	weightSummary = 1
)

// Document is one operation as the index sees it.
type Document struct {
	Key      domain.OperationKey
	ToolName string
	Method   string
	Path     string
	Summary  string
	Tags     []string
	Effect   domain.Effect
}

// Filters narrow the candidate set before scoring. They are constraints, not
// signals: an operation a filter excludes is not a worse match, it is not a
// match, and no score may bring it back (this is what stops a search result
// from becoming a way around a filter).
type Filters struct {
	Tags    []string
	Methods []string
	Effects []domain.Effect
}

// Result is one ranked operation.
type Result struct {
	Key      domain.OperationKey `json:"key"`
	ToolName string              `json:"toolName,omitempty"`
	Method   string              `json:"method"`
	Path     string              `json:"pathTemplate"`
	Summary  string              `json:"summary,omitempty"`
	Tags     []string            `json:"tags,omitempty"`
	Effect   domain.Effect       `json:"effect"`
	Score    float64             `json:"score"`
}

// Index is an immutable inverted index over a catalog snapshot.
type Index struct {
	documents []Document
	// postings maps a term to the documents containing it, with the weighted
	// term frequency in each.
	postings map[string][]posting
	// lengths is each document's weighted token count, and average its mean:
	// BM25 needs both to stop long documents from winning on volume.
	lengths []float64
	average float64
	// tags is the tag vocabulary with document counts, in a stable order.
	tags []TagCount
}

type posting struct {
	document  int
	frequency float64
}

// TagCount is one tag and how many operations carry it.
type TagCount struct {
	Tag   string `json:"tag"`
	Count int    `json:"count"`
}

// Build indexes the documents. Input order is preserved as the document order,
// and every map is read out sorted, so the result does not depend on Go's map
// iteration (Constitution IV).
func Build(documents []Document) *Index {
	index := &Index{
		documents: append([]Document(nil), documents...),
		postings:  make(map[string][]posting),
		lengths:   make([]float64, len(documents)),
	}

	tagCounts := map[string]int{}
	var total float64

	for i := range documents {
		document := &documents[i]
		frequencies := map[string]float64{}
		add := func(text string, weight float64) {
			for _, token := range textnorm.Tokens(text) {
				frequencies[token] += weight
				index.lengths[i] += weight
			}
		}
		// The method is indexed as a word so that "delete pet" finds a DELETE
		// even when the name does not contain the verb.
		add(document.ToolName, weightName)
		add(string(document.Key), weightName)
		add(document.Method, weightName)
		add(document.Path, weightPath)
		add(document.Summary, weightSummary)
		for _, tag := range document.Tags {
			add(tag, weightTag)
			tagCounts[tag]++
		}

		for _, term := range sortedTerms(frequencies) {
			index.postings[term] = append(index.postings[term], posting{document: i, frequency: frequencies[term]})
		}
		total += index.lengths[i]
	}

	if len(documents) > 0 {
		index.average = total / float64(len(documents))
	}
	index.tags = sortedTagCounts(tagCounts)
	return index
}

// Len reports how many operations are indexed.
func (ix *Index) Len() int { return len(ix.documents) }

// Tags returns the tag vocabulary with counts, most frequent first, then
// alphabetically (FR-48's list_tags).
func (ix *Index) Tags() []TagCount { return append([]TagCount(nil), ix.tags...) }

// Search ranks the operations matching query under filters.
//
// A query that matches nothing returns nothing. There is no "closest"
// operation: inventing one is how an agent ends up calling the wrong endpoint
// confidently (Principle I).
func (ix *Index) Search(query string, filters Filters, limit int) []Result {
	terms := textnorm.Tokens(query)
	scores := make(map[int]float64)

	for _, term := range terms {
		postings := ix.postings[term]
		if len(postings) == 0 {
			continue
		}
		idf := ix.idf(len(postings))
		for _, entry := range postings {
			if !ix.allows(entry.document, filters) {
				continue
			}
			scores[entry.document] += idf * ix.saturate(entry.frequency, entry.document)
		}
	}

	// With no query terms, search degrades into browsing: every operation the
	// filters allow, in catalog order. That is more useful than nothing and
	// still deterministic.
	if len(terms) == 0 {
		for i := range ix.documents {
			if ix.allows(i, filters) {
				scores[i] = 0
			}
		}
	}

	return ix.rank(scores, limit)
}

// idf is the BM25 inverse document frequency, floored at zero: a term in most
// documents carries no information, and a negative weight would let it push
// matches *down*.
func (ix *Index) idf(matching int) float64 {
	n := float64(len(ix.documents))
	value := math.Log(1 + (n-float64(matching)+0.5)/(float64(matching)+0.5))
	if value < 0 {
		return 0
	}
	return value
}

// saturate is BM25's term-frequency saturation: the tenth occurrence of a word
// says much less than the second, and a long document needs more occurrences to
// mean the same thing.
func (ix *Index) saturate(frequency float64, document int) float64 {
	length := ix.lengths[document]
	norm := 1.0
	if ix.average > 0 {
		norm = 1 - b + b*(length/ix.average)
	}
	return frequency * (k1 + 1) / (frequency + k1*norm)
}

func (ix *Index) allows(document int, filters Filters) bool {
	candidate := &ix.documents[document]

	if len(filters.Methods) > 0 && !containsFold(filters.Methods, candidate.Method) {
		return false
	}
	if len(filters.Effects) > 0 {
		var found bool
		for _, effect := range filters.Effects {
			if effect == candidate.Effect {
				found = true
			}
		}
		if !found {
			return false
		}
	}
	if len(filters.Tags) > 0 {
		var found bool
		for _, wanted := range filters.Tags {
			if containsFold(candidate.Tags, wanted) {
				found = true
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// rank sorts by score and truncates.
//
// The tie-break is the operation key, ascending. It is arbitrary but it is
// *stated*: two operations that score identically must come back in the same
// order on every run, or a benchmark measures the weather.
func (ix *Index) rank(scores map[int]float64, limit int) []Result {
	documents := make([]int, 0, len(scores))
	for document := range scores {
		documents = append(documents, document)
	}
	sort.Slice(documents, func(i, j int) bool {
		left, right := documents[i], documents[j]
		if scores[left] != scores[right] {
			return scores[left] > scores[right]
		}
		return ix.documents[left].Key < ix.documents[right].Key
	})

	if limit > 0 && len(documents) > limit {
		documents = documents[:limit]
	}

	results := make([]Result, 0, len(documents))
	for _, document := range documents {
		candidate := &ix.documents[document]
		results = append(results, Result{
			Key:      candidate.Key,
			ToolName: candidate.ToolName,
			Method:   candidate.Method,
			Path:     candidate.Path,
			Summary:  candidate.Summary,
			Tags:     append([]string(nil), candidate.Tags...),
			Effect:   candidate.Effect,
			Score:    round(scores[document]),
		})
	}
	return results
}

// round keeps a score stable across platforms: the last bits of a float64 sum
// depend on addition order, and a golden test should not.
func round(score float64) float64 {
	return math.Round(score*1e6) / 1e6
}

func sortedTerms(frequencies map[string]float64) []string {
	terms := make([]string, 0, len(frequencies))
	for term := range frequencies {
		terms = append(terms, term)
	}
	sort.Strings(terms)
	return terms
}

func sortedTagCounts(counts map[string]int) []TagCount {
	out := make([]TagCount, 0, len(counts))
	for tag, count := range counts {
		out = append(out, TagCount{Tag: tag, Count: count})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Tag < out[j].Tag
	})
	return out
}

func containsFold(values []string, want string) bool {
	for _, value := range values {
		if strings.EqualFold(value, want) {
			return true
		}
	}
	return false
}
