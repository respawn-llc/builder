package main

type goPackage struct {
	ImportPath   string
	Dir          string
	TestGoFiles  []string
	XTestGoFiles []string
}

type testJob struct {
	packagePath            string
	testNames              []string
	testRootCount          int
	estimatedWeight        int
	packageEstimatedWeight int
	packageShardIndex      int
}

type testRoot struct {
	name   string
	weight int
}

type testShard struct {
	names  []string
	weight int
}

type jobEvent struct {
	Event           string  `json:"event"`
	Package         string  `json:"package"`
	ShardID         string  `json:"shardID"`
	RootListSHA256  string  `json:"rootListSHA256,omitempty"`
	TestRoots       int     `json:"testRoots"`
	EstimatedWeight int     `json:"estimatedWeight"`
	PackageWeight   int     `json:"packageWeight"`
	PackageShard    int     `json:"packageShard"`
	StartedSecs     float64 `json:"startedSecs,omitempty"`
	ElapsedSecs     float64 `json:"elapsedSecs,omitempty"`
	Failed          bool    `json:"failed,omitempty"`
}
