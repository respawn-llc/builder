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

func TestOptionalExactStringPreservesPresentWhitespace(t *testing.T) {
	if OptionalExactString(" \t ") != nil {
		t.Fatal("blank exact optional string is present")
	}
	value := OptionalExactString(" value \n\n")
	if value == nil || *value != " value \n\n" {
		t.Fatalf("exact optional string = %#v, want lexical value", value)
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

func TestOptionalExtractorsPreservePresenceAndNormalization(t *testing.T) {
	if value, present := OptionalValue[int](nil); present || value != 0 {
		t.Fatalf("absent optional value = (%d, %t)", value, present)
	}
	number := 7
	if value, present := OptionalValue(&number); !present || value != 7 {
		t.Fatalf("present optional value = (%d, %t)", value, present)
	}

	type namedString string
	exact := namedString(" value \n")
	if value, present := OptionalExact(&exact); !present || value != " value \n" {
		t.Fatalf("exact optional string = (%q, %t)", value, present)
	}
	blank := namedString(" \t ")
	if value, present := OptionalTrimmed(&blank); present || value != "" {
		t.Fatalf("blank trimmed optional string = (%q, %t)", value, present)
	}
	trimmed := namedString(" value ")
	if value, present := OptionalTrimmed(&trimmed); !present || value != "value" {
		t.Fatalf("trimmed optional string = (%q, %t)", value, present)
	}
	first := namedString(" ")
	second := namedString(" second ")
	if value, present := FirstOptionalTrimmed(nil, &first, &second); !present || value != "second" {
		t.Fatalf("first optional trimmed string = (%q, %t)", value, present)
	}
}

func TestValueCreatesPresentValue(t *testing.T) {
	value := Value("present")
	if value == nil || *value != "present" {
		t.Fatalf("value = %#v, want present pointer", value)
	}
}

func TestEqualOptionalComparesPresenceAndValue(t *testing.T) {
	one, anotherOne, two := 1, 1, 2
	for name, testCase := range map[string]struct {
		left  *int
		right *int
		want  bool
	}{
		"both absent":       {want: true},
		"left absent":       {right: &one},
		"right absent":      {left: &one},
		"equal present":     {left: &one, right: &anotherOne, want: true},
		"different present": {left: &one, right: &two},
	} {
		t.Run(name, func(t *testing.T) {
			if got := EqualOptional(testCase.left, testCase.right); got != testCase.want {
				t.Fatalf("EqualOptional() = %t, want %t", got, testCase.want)
			}
		})
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
