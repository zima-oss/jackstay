package inventory

// Page is the items of one page: number counts from 0, size items a page.
func Page(items []string, number, size int) []string {
	if size <= 0 || number < 0 {
		return nil
	}
	start := number * size
	end := start + size
	if end > len(items) {
		return nil
	}
	return items[start:end]
}
