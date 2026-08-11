package blackbox

type RunFixture struct {
	Config         []byte
	WorkspaceFiles []WorkspaceFixtureFile
}

type WorkspaceFixtureFile struct {
	Path    string
	Content []byte
}
