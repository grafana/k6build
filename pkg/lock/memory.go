package lock

import (
	"context"
	"sync"
)

// MemoryLock is a lock service that uses an in-memory map to store locks
type MemoryLock struct {
	mutexes sync.Map
}

// NewMemoryLock creates a new MemoryLock
func NewMemoryLock() *MemoryLock {
	return &MemoryLock{}
}

// Lock reserves a lock for the given id and returns a function that will release the lock
func (l *MemoryLock) Lock(_ context.Context, id string) (func(context.Context) error, error) {
	value, _ := l.mutexes.LoadOrStore(id, &sync.Mutex{})
	mtx, _ := value.(*sync.Mutex)
	mtx.Lock()

	return func(_ context.Context) error {
		l.mutexes.Delete(id)
		mtx.Unlock()
		return nil
	}, nil
}
