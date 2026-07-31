package textutil

import "fmt"

func Ordinal(value int) string {
	if value <= 0 {
		return "0th"
	}
	if value%100 >= 11 && value%100 <= 13 {
		return fmt.Sprintf("%dth", value)
	}
	switch value % 10 {
	case 1:
		return fmt.Sprintf("%dst", value)
	case 2:
		return fmt.Sprintf("%dnd", value)
	case 3:
		return fmt.Sprintf("%drd", value)
	default:
		return fmt.Sprintf("%dth", value)
	}
}
