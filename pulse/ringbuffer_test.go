package pulse

import (
	"sync"
	"testing"
)

func TestRingBuffer_PushAndLen(t *testing.T) {
	rb := NewRingBuffer[int](5)

	if rb.Len() != 0 {
		t.Fatalf("expected len 0, got %d", rb.Len())
	}

	rb.Push(1)
	rb.Push(2)
	rb.Push(3)

	if rb.Len() != 3 {
		t.Fatalf("expected len 3, got %d", rb.Len())
	}
}

func TestRingBuffer_GetAll_NotFull(t *testing.T) {
	rb := NewRingBuffer[int](5)
	rb.Push(10)
	rb.Push(20)
	rb.Push(30)

	all := rb.GetAll()
	expected := []int{10, 20, 30}

	if len(all) != len(expected) {
		t.Fatalf("expected %d items, got %d", len(expected), len(all))
	}
	for i, v := range expected {
		if all[i] != v {
			t.Errorf("index %d: expected %d, got %d", i, v, all[i])
		}
	}
}

func TestRingBuffer_GetAll_Full(t *testing.T) {
	rb := NewRingBuffer[int](3)
	rb.Push(1)
	rb.Push(2)
	rb.Push(3)
	rb.Push(4) // overwrites 1
	rb.Push(5) // overwrites 2

	all := rb.GetAll()
	expected := []int{3, 4, 5}

	if len(all) != len(expected) {
		t.Fatalf("expected %d items, got %d", len(expected), len(all))
	}
	for i, v := range expected {
		if all[i] != v {
			t.Errorf("index %d: expected %d, got %d", i, v, all[i])
		}
	}
}

func TestRingBuffer_GetLast(t *testing.T) {
	rb := NewRingBuffer[int](5)
	rb.Push(10)
	rb.Push(20)
	rb.Push(30)
	rb.Push(40)
	rb.Push(50)

	last := rb.GetLast(3)
	expected := []int{50, 40, 30} // most recent first

	if len(last) != len(expected) {
		t.Fatalf("expected %d items, got %d", len(expected), len(last))
	}
	for i, v := range expected {
		if last[i] != v {
			t.Errorf("index %d: expected %d, got %d", i, v, last[i])
		}
	}
}

func TestRingBuffer_GetLast_MoreThanSize(t *testing.T) {
	rb := NewRingBuffer[int](5)
	rb.Push(1)
	rb.Push(2)

	last := rb.GetLast(10) // request more than available
	if len(last) != 2 {
		t.Fatalf("expected 2 items, got %d", len(last))
	}
}

func TestRingBuffer_GetLast_AfterWrap(t *testing.T) {
	rb := NewRingBuffer[int](3)
	rb.Push(1)
	rb.Push(2)
	rb.Push(3)
	rb.Push(4) // overwrites 1
	rb.Push(5) // overwrites 2

	last := rb.GetLast(3)
	expected := []int{5, 4, 3}

	if len(last) != len(expected) {
		t.Fatalf("expected %d items, got %d", len(expected), len(last))
	}
	for i, v := range expected {
		if last[i] != v {
			t.Errorf("index %d: expected %d, got %d", i, v, last[i])
		}
	}
}

func TestRingBuffer_Reset(t *testing.T) {
	rb := NewRingBuffer[int](5)
	rb.Push(1)
	rb.Push(2)
	rb.Push(3)
	rb.Reset()

	if rb.Len() != 0 {
		t.Fatalf("expected len 0 after reset, got %d", rb.Len())
	}
	all := rb.GetAll()
	if len(all) != 0 {
		t.Fatalf("expected 0 items after reset, got %d", len(all))
	}
}

func TestRingBuffer_Filter(t *testing.T) {
	rb := NewRingBuffer[int](10)
	for i := 1; i <= 10; i++ {
		rb.Push(i)
	}

	evens := rb.Filter(func(v int) bool { return v%2 == 0 })
	expected := []int{2, 4, 6, 8, 10}

	if len(evens) != len(expected) {
		t.Fatalf("expected %d items, got %d", len(expected), len(evens))
	}
	for i, v := range expected {
		if evens[i] != v {
			t.Errorf("index %d: expected %d, got %d", i, v, evens[i])
		}
	}
}

func TestRingBuffer_Empty(t *testing.T) {
	rb := NewRingBuffer[int](5)

	if all := rb.GetAll(); all != nil {
		t.Fatalf("expected nil for empty GetAll, got %v", all)
	}
	if last := rb.GetLast(3); last != nil {
		t.Fatalf("expected nil for empty GetLast, got %v", last)
	}
	if last := rb.GetLast(0); last != nil {
		t.Fatalf("expected nil for GetLast(0), got %v", last)
	}
}

func TestRingBuffer_ConcurrentPush(t *testing.T) {
	rb := NewRingBuffer[int](1000)
	var wg sync.WaitGroup

	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(start int) {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				rb.Push(start*100 + j)
			}
		}(i)
	}

	wg.Wait()

	if rb.Len() != 1000 {
		t.Fatalf("expected 1000, got %d", rb.Len())
	}
}

func TestRingBuffer_DefaultCapacity(t *testing.T) {
	rb := NewRingBuffer[int](0)
	if rb.capacity != 1024 {
		t.Fatalf("expected default capacity 1024, got %d", rb.capacity)
	}
}

func BenchmarkRingBuffer_Push(b *testing.B) {
	rb := NewRingBuffer[int](100000)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		rb.Push(i)
	}
}

func BenchmarkRingBuffer_GetLast(b *testing.B) {
	rb := NewRingBuffer[int](100000)
	for i := 0; i < 100000; i++ {
		rb.Push(i)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		rb.GetLast(100)
	}
}
