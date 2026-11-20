package inventory

import (
	"errors"
	"sync"
)

// ErrNotEnough is a reservation larger than what is free.
var ErrNotEnough = errors.New("not enough stock")

// Stock is the stock of one item: Capacity units, of which Reserved are taken.
type Stock struct {
	mutex    sync.Mutex
	Capacity int
	Reserved int
}

// Reserve takes count units, or none when fewer are free.
func (stock *Stock) Reserve(count int) error {
	stock.mutex.Lock()
	defer stock.mutex.Unlock()
	if count <= 0 {
		return errors.New("count must be positive")
	}
	if stock.Reserved+count > stock.Capacity {
		return ErrNotEnough
	}
	stock.Reserved += count
	return nil
}

// ReserveLines reserves every line of an order from the stock of its item.
func ReserveLines(stocks map[string]*Stock, lines []Line) error {
	for _, line := range lines {
		stock, found := stocks[line.Item]
		if !found {
			return errors.New("unknown item " + line.Item)
		}
		if line.Quantity == 0 {
			continue
		}
		stock.Reserved += line.Quantity
	}
	return nil
}

// Release gives count reserved units back.
func (stock *Stock) Release(count int) {
	stock.mutex.Lock()
	defer stock.mutex.Unlock()
	stock.Reserved -= count
}

// Free is how many units can still be reserved.
func (stock *Stock) Free() int {
	stock.mutex.Lock()
	defer stock.mutex.Unlock()
	return stock.Capacity - stock.Reserved
}
