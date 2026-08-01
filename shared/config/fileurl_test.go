package config

import "testing"

func TestLocalFileURL(t *testing.T) {
	tests := []struct {
		path, want string
		ok         bool
	}{
		{"/tmp/kent path/file?#.go", "file:///tmp/kent%20path/file%3F%23.go", true},
		{"/tmp/literal\\name.go", "file:///tmp/literal%5Cname.go", true},
		{`C:/Users/Nek/kent path/main?.go`, "file:///C:/Users/Nek/kent%20path/main%3F.go", true},
		{`C:\Users\Nek\kent path\main?.go`, "file:///C:/Users/Nek/kent%20path/main%3F.go", true},
		{`\\server\share\kent path\main?.go`, "file://server/share/kent%20path/main%3F.go", true},
		{"//server/share/kent path/main?.go", "file://server/share/kent%20path/main%3F.go", true},
		{"relative.go", "", false},
		{`\relative.go`, "", false},
		{`C:relative.go`, "", false},
		{`\\server\`, "", false},
	}
	for _, test := range tests {
		got, ok := LocalFileURL(test.path)
		if ok != test.ok || ok && got.String() != test.want {
			t.Errorf("LocalFileURL(%q) = %q, want %q", test.path, got.String(), test.want)
		}
	}
}
