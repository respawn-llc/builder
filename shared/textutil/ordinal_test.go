package textutil

import "testing"

func TestOrdinal(t *testing.T) {
	t.Parallel()
	tests := map[int]string{
		0:   "0th",
		1:   "1st",
		2:   "2nd",
		3:   "3rd",
		4:   "4th",
		11:  "11th",
		12:  "12th",
		13:  "13th",
		21:  "21st",
		102: "102nd",
	}
	for value, want := range tests {
		if got := Ordinal(value); got != want {
			t.Fatalf("Ordinal(%d) = %q, want %q", value, got, want)
		}
	}
}
