package domain

import "testing"

func TestNewOperationKey(t *testing.T) {
	tests := []struct {
		name         string
		namespace    string
		method       string
		pathTemplate string
		want         OperationKey
	}{
		{"empty namespace defaults", "", "get", "/pets", "default:GET:/pets"},
		{"namespace and lowercase method", "petstore", "post", "/pets/{petId}", "petstore:POST:/pets/{petId}"},
		{"method already uppercase", "ns", "DELETE", "/pets/{petId}", "ns:DELETE:/pets/{petId}"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := NewOperationKey(tt.namespace, tt.method, tt.pathTemplate)
			if got != tt.want {
				t.Errorf("NewOperationKey(%q, %q, %q) = %q, want %q",
					tt.namespace, tt.method, tt.pathTemplate, got, tt.want)
			}
		})
	}
}
