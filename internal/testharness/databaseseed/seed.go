package databaseseed

import (
	"io/fs"
)

type Seed struct {
	contents []byte
	mode     fs.FileMode
}
