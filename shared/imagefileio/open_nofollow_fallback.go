//go:build !darwin && !linux && !windows

package imagefileio

import (
	"errors"
	"os"
)

func openReadOnlyNoFollow(path string) (*os.File, error) {
	return nil, errors.New("safe image file opening is unsupported on this platform")
}

func setReadBlocking(*os.File) error {
	return nil
}
