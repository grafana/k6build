// Package lock defines the interface of a lock service
package lock

import (
	"context"
	"errors"
)

var (
	ErrCofig   = errors.New("error configuring") //nolint:revive
	ErrLocking = errors.New("error locking")     //nolint:revive
)

// Lock defines the interface for a lock service
type Lock interface {
	// Lock reserves a lock for the given id and returns a function that releases the lock
	// While holding the lock, no other process should be able to reserve the same id.
	Lock(ctx context.Context, id string) (func(context.Context) error, error)
	// Try attempts to reserve a lock for a given id. Returns a bool indicating if it could reserve it.
	// If the lock was acquired, returns a function that releases the lock.
	Try(ctx context.Context, id string) (bool, func(context.Context) error, error)
}
