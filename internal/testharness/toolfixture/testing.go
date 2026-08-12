package toolfixture

type TestingT interface {
	Helper()
	Fatalf(format string, args ...any)
}
