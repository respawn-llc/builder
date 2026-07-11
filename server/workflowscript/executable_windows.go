//go:build windows

package workflowscript

import "os"

func scriptFileRunnable(os.FileInfo) bool {
	return true
}
