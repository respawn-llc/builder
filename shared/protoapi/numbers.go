package protoapi

import (
	"fmt"
	"math"
)

func projectInt32(value int, field string) (int32, error) {
	if value < math.MinInt32 || value > math.MaxInt32 {
		return 0, fmt.Errorf("%s is out of int32 range", field)
	}
	return int32(value), nil
}
