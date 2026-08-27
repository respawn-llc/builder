package app

import "testing"

func TestProjectedUIModelReceivesNativeProgressBarSetting(t *testing.T) {
	model := newProjectedTestUIModel(nil, WithUINativeProgressBar(false))
	if model.tuiNativeProgressBar {
		t.Fatal("native progress bar setting = true, want false")
	}
}
