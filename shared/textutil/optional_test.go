package textutil

import "testing"

func TestOptionalTrimmedStringNormalizesPresence(t *testing.T) {
	if OptionalTrimmedString(" \t ") != nil {
		t.Fatal("blank optional string is present")
	}
	value := OptionalTrimmedString(" value ")
	if value == nil || *value != "value" {
		t.Fatalf("optional string = %#v, want value", value)
	}
}

func TestPointerCopiesOptionalValue(t *testing.T) {
	if Pointer[int](nil) != nil {
		t.Fatal("nil pointer copy is not nil")
	}
	source := 7
	copied := Pointer(&source)
	source = 9
	if copied == nil || *copied != 7 {
		t.Fatalf("pointer copy = %+v, want independent value 7", copied)
	}
}

func TestEqualOptionalComparesPresenceAndValue(t *testing.T) {
	one := 1
	anotherOne := 1
	two := 2
	if !EqualOptional[int](nil, nil) {
		t.Fatal("two absent values are not equal")
	}
	if EqualOptional(nil, &one) || EqualOptional(&one, nil) {
		t.Fatal("present and absent values are equal")
	}
	if !EqualOptional(&one, &anotherOne) {
		t.Fatal("distinct pointers with equal values are not equal")
	}
	if EqualOptional(&one, &two) {
		t.Fatal("different values are equal")
	}
}

func TestCompareOptionalOrdersAbsentValuesFirst(t *testing.T) {
	one := 1
	two := 2
	cases := []struct {
		name  string
		left  *int
		right *int
		want  int
	}{
		{name: "both absent", want: 0},
		{name: "left absent", right: &one, want: -1},
		{name: "right absent", left: &one, want: 1},
		{name: "equal", left: &one, right: &one, want: 0},
		{name: "ordered", left: &one, right: &two, want: -1},
		{name: "reversed", left: &two, right: &one, want: 1},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			if got := CompareOptional(tt.left, tt.right); got != tt.want {
				t.Fatalf("CompareOptional(%v, %v) = %d, want %d", tt.left, tt.right, got, tt.want)
			}
		})
	}
}
