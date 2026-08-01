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
		normalized := make([]byte, 0, len(path)+1)
		normalized = append(normalized, '/')
		for index := 0; index < len(path); index++ {
			if isPathSeparator(path[index]) {
				normalized = append(normalized, '/')
			} else {
				normalized = append(normalized, path[index])
			}
		}
		return url.URL{Scheme: "file", Path: string(normalized)}, true
	}
	return url.URL{}, false
}
func uncFileURL(path string) (url.URL, bool) {
	index := 2
	server := make([]byte, 0, len(path))
	for index < len(path) && !isPathSeparator(path[index]) {
		server = append(server, path[index])
		index++
	}
	if len(server) == 0 || index >= len(path) {
		return url.URL{}, false
	}
	index++
	if index >= len(path) || isPathSeparator(path[index]) {
		return url.URL{}, false
	}
	share := make([]byte, 0, len(path)-index)
	for index < len(path) && !isPathSeparator(path[index]) {
		share = append(share, path[index])
		index++
	}
	if len(share) == 0 {
		return url.URL{}, false
	}
	uriPath := make([]byte, 1, len(path)+1)
	uriPath[0] = '/'
	uriPath = append(uriPath, share...)
	for index < len(path) {
		if isPathSeparator(path[index]) {
			uriPath = append(uriPath, '/')
		} else {
			uriPath = append(uriPath, path[index])
		}
		index++
	}
	return url.URL{Scheme: "file", Host: string(server), Path: string(uriPath)}, true
}

func isPathSeparator(char byte) bool {
	return char == '/' || char == '\\'
}

func isASCIILetter(char byte) bool {
	return (char >= 'A' && char <= 'Z') || (char >= 'a' && char <= 'z')
}
