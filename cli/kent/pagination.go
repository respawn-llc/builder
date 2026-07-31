package main

import (
	"fmt"
	"io"
)

const nextPageTokenLineFormat = "Next page token: `%s`"
const nextOffsetLineFormat = "Next offset: `%d`"

func nextPageTokenLine(token string) string {
	return fmt.Sprintf(nextPageTokenLineFormat, token)
}

func writeNextPageToken(stderr io.Writer, token string) error {
	return writePaginationContinuation(stderr, nextPageTokenLine(token))
}

func nextOffsetLine(offset int) string {
	return fmt.Sprintf(nextOffsetLineFormat, offset)
}

func writeNextOffset(stderr io.Writer, offset int) error {
	return writePaginationContinuation(stderr, nextOffsetLine(offset))
}

func writePaginationContinuation(stderr io.Writer, line string) error {
	_, err := fmt.Fprintln(stderr, line)
	return err
}
