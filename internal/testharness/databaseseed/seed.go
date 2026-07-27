package databaseseed

import (
	"io/fs"
	"sync"
)

type Seed struct {
	contents []byte
	mode     fs.FileMode
}

const CurrentMetadataDatabaseRelativePath = "db/main.sqlite3"

var currentMetadataDatabaseSeed struct {
	once sync.Once
	seed Seed
	err  error
}
