package setcomparison

import "testing"

func TestEqual(t *testing.T) {
	tests := []struct {
		name  string
		left  []string
		right []string
		want  bool
	}{
		{name: "same order", left: []string{"a", "b"}, right: []string{"a", "b"}, want: true},
		{name: "different order", left: []string{"a", "b"}, right: []string{"b", "a"}, want: true},
		{name: "different member", left: []string{"a", "b"}, right: []string{"a", "c"}},
		{name: "different length", left: []string{"a"}, right: []string{"a", "b"}},
		{name: "duplicate input", left: []string{"a", "a"}, right: []string{"a", "a"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := Equal(test.left, test.right); got != test.want {
				t.Fatalf("Equal(%v, %v) = %t, want %t", test.left, test.right, got, test.want)
			}
		})
	}
}
