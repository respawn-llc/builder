//go:build !windows

package workflowscript

import "os"

func scriptFileRunnable(info os.FileInfo) bool {
	return info.Mode().Perm()&0o111 != 0
}
