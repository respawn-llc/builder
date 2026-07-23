package app

import "testing"

func TestStatusSubscriptionRemainingClampsUsedPercent(t *testing.T) {
	tests := []struct {
		name string
		used float64
		want float64
	}{
		{name: "negative used", used: -10, want: 100},
		{name: "partial used", used: 25, want: 75},
		{name: "over used", used: 150, want: 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := statusSubscriptionRemaining(tt.used)
			if got != tt.want {
				t.Fatalf("remaining = %.1f, want %.1f", got, tt.want)
			}
		})
	}
}

func TestStatusSubscriptionBarWidthForLine(t *testing.T) {
	tests := []struct {
		name     string
		width    int
		label    string
		leftText string
		metaText string
		want     int
	}{
		{name: "wide clamps to max", width: 120, label: "weekly", leftText: "75% left", want: statusSubscriptionBarMaxWidth},
		{name: "narrow keeps minimum", width: 20, label: "weekly", leftText: "75% left", want: 4},
		{name: "metadata consumes space", width: 40, label: "weekly", leftText: "75% left", metaText: "resets soon", want: 6},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := statusSubscriptionBarWidthForLine(tt.width, tt.label, tt.leftText, tt.metaText)
			if got != tt.want {
				t.Fatalf("bar width = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestStatusSubscriptionBarWidthBounds(t *testing.T) {
	if got := statusSubscriptionBarWidth(3); got != 4 {
		t.Fatalf("small width = %d, want 4", got)
	}
	if got := statusSubscriptionBarWidth(500); got != statusSubscriptionBarMaxWidth {
		t.Fatalf("wide width = %d, want %d", got, statusSubscriptionBarMaxWidth)
	}
}
