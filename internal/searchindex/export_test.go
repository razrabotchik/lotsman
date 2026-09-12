package searchindex

import "sort"

// Dump exposes the index's internal state for the golden test. The golden
// pins the postings and lengths, not only the search results: two different
// indexes can agree on one query and disagree on the next.
type Dump struct {
	Documents []string            `json:"documents"`
	Postings  map[string][]string `json:"postings"`
	Lengths   map[string]float64  `json:"lengths"`
	Average   float64             `json:"averageLength"`
	Tags      []TagCount          `json:"tags"`
}

// DumpForTest renders the index deterministically.
func (ix *Index) DumpForTest() Dump {
	out := Dump{
		Postings: map[string][]string{},
		Lengths:  map[string]float64{},
		Average:  round(ix.average),
		Tags:     ix.Tags(),
	}
	for i := range ix.documents {
		key := string(ix.documents[i].Key)
		out.Documents = append(out.Documents, key)
		out.Lengths[key] = ix.lengths[i]
	}
	for term, postings := range ix.postings {
		entries := make([]string, 0, len(postings))
		for _, entry := range postings {
			entries = append(entries, string(ix.documents[entry.document].Key))
		}
		sort.Strings(entries)
		out.Postings[term] = entries
	}
	return out
}
