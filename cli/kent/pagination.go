package main

import (
	"fmt"
	"io"
)

const nextPageTokenLineFormat = "Next page token: `%s`"

func nextPageTokenLine(token string) string {
	return fmt.Sprintf(nextPageTokenLineFormat, token)
}

func writeNextPageToken(stderr io.Writer, token string) error {
	_, err := fmt.Fprintln(stderr, nextPageTokenLine(token))
	return err
}
