package metadata

func SQLiteBoolInt64(value bool) int64 {
	if value {
		return 1
	}
	return 0
}
