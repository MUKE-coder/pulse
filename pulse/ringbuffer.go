package pulse

import (
	"sync"
	"sync/atomic"
)

// RingBuffer is a lock-free, fixed-capacity circular buffer for high-throughput
// metric ingestion. When full, the oldest items are silently overwritten.
// Push is lock-free (atomic CAS on head). Read operations take a read lock.
type RingBuffer[T any] struct {
	data     []T
	head     atomic.Int64
	size     atomic.Int64
	capacity int64
	mu       sync.RWMutex // protects reads for consistent snapshots
}

// NewRingBuffer creates a new RingBuffer with the given capacity.
func NewRingBuffer[T any](capacity int) *RingBuffer[T] {
	if capacity <= 0 {
		capacity = 1024
	}
	return &RingBuffer[T]{
		data:     make([]T, capacity),
		capacity: int64(capacity),
	}
}

// Push adds an item to the ring buffer. If full, the oldest item is overwritten.
// This method is safe for concurrent use.
func (rb *RingBuffer[T]) Push(item T) {
	idx := rb.head.Add(1) - 1
	pos := idx % rb.capacity

	rb.mu.Lock()
	rb.data[pos] = item
	rb.mu.Unlock()

	// Update size (cap at capacity)
	for {
		current := rb.size.Load()
		newSize := current + 1
		if newSize > rb.capacity {
			newSize = rb.capacity
		}
		if rb.size.CompareAndSwap(current, newSize) {
			break
		}
	}
}

// Len returns the number of items currently in the buffer.
func (rb *RingBuffer[T]) Len() int {
	return int(rb.size.Load())
}

// GetAll returns all items in the buffer ordered from oldest to newest.
func (rb *RingBuffer[T]) GetAll() []T {
	rb.mu.RLock()
	defer rb.mu.RUnlock()

	size := rb.size.Load()
	if size == 0 {
		return nil
	}

	head := rb.head.Load()
	result := make([]T, size)

	if size < rb.capacity {
		// Buffer not yet full — items are at indices 0..size-1
		copy(result, rb.data[:size])
	} else {
		// Buffer is full — oldest item is at head % capacity
		start := head % rb.capacity
		// Copy from start to end of backing array
		n := copy(result, rb.data[start:])
		// Copy from beginning of backing array to start
		copy(result[n:], rb.data[:start])
	}

	return result
}

// GetLast returns the last n items (most recent first).
func (rb *RingBuffer[T]) GetLast(n int) []T {
	rb.mu.RLock()
	defer rb.mu.RUnlock()

	size := int(rb.size.Load())
	if size == 0 || n <= 0 {
		return nil
	}
	if n > size {
		n = size
	}

	head := rb.head.Load()
	result := make([]T, n)

	// Walk backwards from head-1
	for i := 0; i < n; i++ {
		idx := (head - 1 - int64(i))
		pos := idx % rb.capacity
		if pos < 0 {
			pos += rb.capacity
		}
		result[i] = rb.data[pos]
	}

	return result
}

// Reset clears the buffer.
func (rb *RingBuffer[T]) Reset() {
	rb.mu.Lock()
	defer rb.mu.Unlock()

	rb.head.Store(0)
	rb.size.Store(0)
	var zero T
	for i := range rb.data {
		rb.data[i] = zero
	}
}

// ForEach iterates over all items from oldest to newest, calling fn for each.
// If fn returns false, iteration stops.
func (rb *RingBuffer[T]) ForEach(fn func(item T) bool) {
	rb.mu.RLock()
	defer rb.mu.RUnlock()

	size := rb.size.Load()
	if size == 0 {
		return
	}

	head := rb.head.Load()

	if size < rb.capacity {
		for i := int64(0); i < size; i++ {
			if !fn(rb.data[i]) {
				return
			}
		}
	} else {
		start := head % rb.capacity
		for i := int64(0); i < rb.capacity; i++ {
			pos := (start + i) % rb.capacity
			if !fn(rb.data[pos]) {
				return
			}
		}
	}
}

// Filter returns all items matching the predicate, ordered oldest to newest.
func (rb *RingBuffer[T]) Filter(predicate func(item T) bool) []T {
	var result []T
	rb.ForEach(func(item T) bool {
		if predicate(item) {
			result = append(result, item)
		}
		return true
	})
	return result
}
