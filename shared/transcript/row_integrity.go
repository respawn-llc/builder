package transcript

type RowIntegrity uint8

const (
	RowIntegrityValid RowIntegrity = iota
	RowIntegrityRecoverableMalformed
	RowIntegrityUnrecoverableMalformed
)

func (integrity RowIntegrity) Valid() bool {
	return integrity == RowIntegrityValid ||
		integrity == RowIntegrityRecoverableMalformed ||
		integrity == RowIntegrityUnrecoverableMalformed
}
