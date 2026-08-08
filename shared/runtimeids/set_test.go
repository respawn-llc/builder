package runtimeids

import "testing"

func TestEqualSets(t *testing.T) {
	type namedID string
	tests := []struct {
		name  string
		left  []namedID
		right []namedID
		want  bool
	}{
		{name: "same order", left: []namedID{"a", "b"}, right: []namedID{"a", "b"}, want: true},
		{name: "different order", left: []namedID{"a", "b"}, right: []namedID{"b", "a"}, want: true},
		{name: "different member", left: []namedID{"a", "b"}, right: []namedID{"a", "c"}},
		{name: "different length", left: []namedID{"a"}, right: []namedID{"a", "b"}},
		{name: "duplicate input", left: []namedID{"a", "a"}, right: []namedID{"a", "a"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := EqualSets(test.left, test.right); got != test.want {
				t.Fatalf("EqualSets(%v, %v) = %t, want %t", test.left, test.right, got, test.want)
			}
		})
	}
}
