package config

import "testing"

func TestLocalFileURL(t *testing.T) {
	accepted := map[string]string{
		"/tmp/kent path/file?#.go":          "file:///tmp/kent%20path/file%3F%23.go",
		"/tmp/literal\\name.go":             "file:///tmp/literal%5Cname.go",
		`C:/Users/Nek/kent path/main?.go`:   "file:///C:/Users/Nek/kent%20path/main%3F.go",
		`C:\Users\Nek\kent path\main?.go`:   "file:///C:/Users/Nek/kent%20path/main%3F.go",
		`\\server\share\kent path\main?.go`: "file://server/share/kent%20path/main%3F.go",
		"//server/share/kent path/main?.go": "file://server/share/kent%20path/main%3F.go",
	}
	for path, want := range accepted {
		got, ok := LocalFileURL(path)
		if !ok || got.String() != want {
			t.Errorf("LocalFileURL(%q) = %q, want %q", path, got.String(), want)
		}
	}
	for _, path := range []string{"relative.go", `\relative.go`, `C:relative.go`} {
		if _, ok := LocalFileURL(path); ok {
			t.Errorf("LocalFileURL(%q) unexpectedly accepted relative path", path)
		}
	}
}
