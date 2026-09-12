package textnorm

import (
	"reflect"
	"testing"
)

func TestTokens(t *testing.T) {
	for _, tt := range []struct {
		in   string
		want []string
	}{
		{"rebuildCache", []string{"rebuild", "cache"}},
		{"rebuild_cache", []string{"rebuild", "cache"}},
		{"/legacy/rebuild-cache", []string{"legacy", "rebuild", "cache"}},
		{"/projects/{projectId}/members", []string{"projects", "project", "id", "members"}},
		{"getAPIKey", []string{"get", "api", "key"}},
		{"", nil},
	} {
		t.Run(tt.in, func(t *testing.T) {
			if got := Tokens(tt.in); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("Tokens(%q) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}
