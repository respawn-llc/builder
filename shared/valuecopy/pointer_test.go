package valuecopy

import "testing"

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
