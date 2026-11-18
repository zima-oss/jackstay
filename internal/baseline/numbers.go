package baseline

import (
	"math"
	"slices"
	"strconv"
	"strings"
)

// Ratio is a number with a fraction, written as the report's readers know it: with a decimal
// point even when it is whole (2.0, not 2).
type Ratio float64

// MarshalJSON writes the shortest text that reads back as the number, with a decimal point.
func (ratio Ratio) MarshalJSON() ([]byte, error) {
	text := strconv.FormatFloat(float64(ratio), 'f', -1, 64)
	if !strings.ContainsAny(text, ".eEn") {
		text += ".0"
	}
	return []byte(text), nil
}

// rounded is value rounded to digits places, half to even on the exact value (as the report
// always rounded).
func rounded(value float64, digits int) Ratio {
	parsed, err := strconv.ParseFloat(strconv.FormatFloat(value, 'f', digits, 64), 64)
	if err != nil {
		return Ratio(value)
	}
	return Ratio(parsed)
}

// ratioOrNil is count/merges rounded to two places, or nil when there is nothing to divide by.
func ratioOrNil(count int, merges *int) *Ratio {
	if merges == nil || *merges == 0 {
		return nil
	}
	ratio := rounded(float64(count)/float64(*merges), 2)
	return &ratio
}

// median is the middle value in whole seconds (half to even), or nil for none.
func median(values []float64) *int {
	if len(values) == 0 {
		return nil
	}
	sorted := slices.Clone(values)
	slices.Sort(sorted)
	middle := len(sorted) / 2
	value := sorted[middle]
	if len(sorted)%2 == 0 {
		value = (sorted[middle-1] + sorted[middle]) / 2
	}
	whole := int(math.RoundToEven(value))
	return &whole
}

// percentile90 is the value at the 90th percentile (nearest rank) in whole seconds, or nil.
func percentile90(sorted []float64) *int {
	if len(sorted) == 0 {
		return nil
	}
	rank := (9*len(sorted) + 9) / 10
	whole := int(math.RoundToEven(sorted[max(0, rank-1)]))
	return &whole
}

func hours(values []float64) Ratio {
	total := 0.0
	for _, value := range values {
		total += value
	}
	return rounded(total/3600, 1)
}

func intPointer(value int) *int { return &value }
