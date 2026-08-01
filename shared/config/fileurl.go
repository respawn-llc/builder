package config

import "net/url"

func LocalFileURL(path string) (url.URL, bool) {
	if len(path) >= 2 && isPathSeparator(path[0]) && isPathSeparator(path[1]) {
		return uncFileURL(path)
	}
	if len(path) > 0 && path[0] == '/' {
		return url.URL{Scheme: "file", Path: path}, true
	}
	if len(path) >= 3 && isASCIILetter(path[0]) && path[1] == ':' && isPathSeparator(path[2]) {
		_, normalized := pathBytes(path, 0, true)
		normalized = append([]byte{'/'}, normalized...)
		return url.URL{Scheme: "file", Path: string(normalized)}, true
	}
	return url.URL{}, false
}
func uncFileURL(path string) (url.URL, bool) {
	index := 2
	index, server := pathBytes(path, index, false)
	if len(server) == 0 || index >= len(path) {
		return url.URL{}, false
	}
	index++
	if index >= len(path) || isPathSeparator(path[index]) {
		return url.URL{}, false
	}
	index, share := pathBytes(path, index, false)
	if len(share) == 0 {
		return url.URL{}, false
	}
	uriPath := append([]byte{'/'}, share...)
	_, remainder := pathBytes(path, index, true)
	uriPath = append(uriPath, remainder...)
	return url.URL{Scheme: "file", Host: string(server), Path: string(uriPath)}, true
}

func pathBytes(path string, index int, normalize bool) (int, []byte) {
	bytes := make([]byte, 0, len(path)-index)
	for index < len(path) && (normalize || !isPathSeparator(path[index])) {
		char := path[index]
		if normalize && isPathSeparator(char) {
			char = '/'
		}
		bytes = append(bytes, char)
		index++
	}
	return index, bytes
}

func isPathSeparator(char byte) bool { return char == '/' || char == '\\' }

func isASCIILetter(char byte) bool {
	return (char >= 'A' && char <= 'Z') || (char >= 'a' && char <= 'z')
}
