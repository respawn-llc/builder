package fileurl

import (
	"net/url"
	"strings"
)

func LocalFileURL(path string) (url.URL, bool) {
	if strings.HasPrefix(path, `\\`) || strings.HasPrefix(path, "//") {
		normalized := strings.ReplaceAll(path, `\`, "/")
		rest := normalized[2:]
		serverEnd := strings.IndexByte(rest, '/')
		if serverEnd <= 0 || serverEnd == len(rest)-1 {
			return url.URL{}, false
		}
		shareAndPath := rest[serverEnd+1:]
		shareEnd := strings.IndexByte(shareAndPath, '/')
		if shareEnd == 0 {
			return url.URL{}, false
		}
		share := shareAndPath
		remainder := ""
		if shareEnd > 0 {
			share = shareAndPath[:shareEnd]
			remainder = shareAndPath[shareEnd:]
		}
		return url.URL{Scheme: "file", Host: rest[:serverEnd], Path: "/" + share + remainder}, true
	}
	if strings.HasPrefix(path, "/") {
		return url.URL{Scheme: "file", Path: path}, true
	}
	if len(path) >= 3 && isASCIILetter(path[0]) && path[1] == ':' && (path[2] == '/' || path[2] == '\\') {
		normalized := strings.ReplaceAll(path, `\`, "/")
		return url.URL{Scheme: "file", Path: "/" + normalized}, true
	}
	return url.URL{}, false
}

func isASCIILetter(char byte) bool {
	return (char >= 'A' && char <= 'Z') || (char >= 'a' && char <= 'z')
}
