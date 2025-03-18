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
	// Lock reserves a lock for the given id and returns a function that will release the lock
	// While holding the lock, no other process should be able to reserve the same id.
	Lock(ctx context.Context, id string) (func(context.Context) error, error)
}
