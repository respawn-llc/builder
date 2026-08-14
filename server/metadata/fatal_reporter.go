package metadata

// FatalReporter is the process-lifetime authority for critical live-metadata
// failures. Implementations retain the first submitted failure.
type FatalReporter interface {
	ReportMetadataFatal(*ClassifiedFailure) bool
	MetadataFatal() *ClassifiedFailure
}
