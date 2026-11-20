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

// Free is how many units can still be reserved.
func (stock *Stock) Free() int {
	stock.mutex.Lock()
	defer stock.mutex.Unlock()
	return stock.Capacity - stock.Reserved
}
