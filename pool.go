package genpool

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"time"
)

// Common errors
var (
	ErrPoolClosed = errors.New("pool is closed")
	ErrPoolFull   = errors.New("pool is at capacity")
	ErrTimeout    = errors.New("operation timed out")
)

// Stats represents pool usage statistics
type Stats struct {
	Created     int64 // Total number of resources created
	Reused      int64 // Number of times resources were reused
	Failed      int64 // Number of failed health checks
	Discarded   int64 // Number of discarded resources
	CurrentSize int   // Current number of resources managed by pool
	Available   int   // Number of resources currently available in the pool
	InUse       int   // Number of resources currently borrowed from the pool
}

// Resource represents a poolable resource with its metadata
type Resource[T any] struct {
	Value       T         // The actual resource
	CreatedAt   time.Time // When the resource was created
	LastUsed    time.Time // When the resource was last borrowed
	LastChecked time.Time // When the resource was last health-checked
	BorrowCount int       // Number of times this resource has been borrowed
}

// Factory defines functions to manage the lifecycle of a pooled resource
type Factory[T any] interface {
	// Create creates a new resource
	Create(ctx context.Context) (T, error)

	// Validate checks if a resource is still valid/healthy
	Validate(resource T) bool

	// Close cleans up a resource when it's removed from the pool
	Close(resource T) error
}

// Pool manages reusable resources of type T
type Pool[T any] interface {
	// Get retrieves a resource from the pool or creates a new one
	Get(ctx context.Context) (T, error)

	// Release returns a resource to the pool
	Release(value T) error

	// Close shuts down the pool and releases all resources
	Close() error

	// Stats returns current pool statistics
	Stats() Stats
}

// SimplePool implements a basic resource pool with health checking
type SimplePool[T any] struct {
	factory        Factory[T]
	resources      []*Resource[T]       // Available resources
	inUseResources map[any]*Resource[T] // Resources currently in use
	maxSize        int                  // Maximum number of resources
	semaphore      chan struct{}        // Controls concurrent resource creation
	zeroval        T                    // Zero value for type T

	healthCheck time.Duration // Interval for health checks
	mu          sync.Mutex
	closed      bool
	ctx         context.Context
	cancel      context.CancelFunc

	// Statistics
	stats Stats
}

// Options configures a SimplePool
type Options struct {
	// MaxSize is the maximum number of resources in the pool
	MaxSize int

	// HealthCheckInterval specifies how often to run health checks on idle resources
	HealthCheckInterval time.Duration
}

// New creates a new pool with the specified factory and options
func New[T any](factory Factory[T], options Options) Pool[T] {
	// Default values for options
	if options.MaxSize <= 0 {
		options.MaxSize = 10 // Default max size
	}
	if options.HealthCheckInterval <= 0 {
		options.HealthCheckInterval = 5 * time.Minute // Default health check interval
	}

	// Check if type T is comparable, as it's used as a map key via any(value)
	var zero T
	if !reflect.TypeOf(zero).Comparable() {
		panic("pool: type T must be comparable to be used as a map key for inUseResources tracking")
	}

	ctx, cancel := context.WithCancel(context.Background())

	p := &SimplePool[T]{
		factory:        factory,
		resources:      make([]*Resource[T], 0, options.MaxSize),
		inUseResources: make(map[any]*Resource[T]),
		maxSize:        options.MaxSize,
		semaphore:      make(chan struct{}, options.MaxSize),
		healthCheck:    options.HealthCheckInterval,
		ctx:            ctx,
		cancel:         cancel,
	}

	// Initialize semaphore
	for i := 0; i < options.MaxSize; i++ {
		p.semaphore <- struct{}{}
	}

	// Start health check routine
	if options.HealthCheckInterval > 0 {
		go p.healthCheckLoop()
	}

	return p
}

// Get retrieves a resource from the pool or creates a new one
func (p *SimplePool[T]) Get(ctx context.Context) (T, error) {
	for { // Loop to retry if a cached resource is invalid
		p.mu.Lock()

		if p.closed {
			p.mu.Unlock()
			return p.zeroval, ErrPoolClosed
		}

		// First try to get an existing resource
		if len(p.resources) > 0 {
			// Take the last resource (LIFO strategy for better cache locality)
			lastIdx := len(p.resources) - 1
			resWrapper := p.resources[lastIdx]
			p.resources = p.resources[:lastIdx]
			p.mu.Unlock() // Unlock before potentially long-running Validate

			// Check if the resource is still valid
			if p.factory.Validate(resWrapper.Value) {
				// Resource is valid, update its metadata
				resWrapper.LastUsed = time.Now()
				resWrapper.BorrowCount++

				p.mu.Lock()
				p.inUseResources[any(resWrapper.Value)] = resWrapper
				p.stats.Reused++
				p.stats.InUse++
				p.stats.Available = len(p.resources)
				p.mu.Unlock()

				return resWrapper.Value, nil
			}

			// Resource is invalid, close and discard it
			_ = p.discardResource(resWrapper, true) // true indicates validation failure

			// Continue to the next iteration of the loop to try again
			continue
		}

		// No resources available, need to acquire a semaphore to create one
		p.mu.Unlock()
		break // Break the loop to proceed to resource creation
	}

	// Try to acquire semaphore with timeout from context
	select {
	case <-p.semaphore:
		// Semaphore acquired, we can create a new resource
	case <-ctx.Done():
		return p.zeroval, ctx.Err()
	}

	// Create a new resource
	value, err := p.factory.Create(ctx)
	if err != nil {
		// Creation failed, return semaphore
		p.semaphore <- struct{}{}
		return p.zeroval, err
	}

	// Resource created successfully
	now := time.Now()
	newResWrapper := &Resource[T]{
		Value:       value,
		CreatedAt:   now,
		LastUsed:    now,
		LastChecked: now,
		BorrowCount: 1,
	}

	p.mu.Lock()
	p.inUseResources[any(value)] = newResWrapper
	p.stats.Created++
	p.stats.InUse++
	p.stats.CurrentSize++
	p.mu.Unlock()

	return value, nil
}

// Release returns a resource to the pool
func (p *SimplePool[T]) Release(value T) error {
	v := reflect.ValueOf(value)
	if !v.IsValid() || (v.Kind() == reflect.Ptr && v.IsNil()) {
		return nil // Ignore zero/nil values
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	if p.closed {
		// If pool is closed, just close the resource
		return p.factory.Close(value)
	}

	// Find the resource in the inUseResources map
	res, found := p.inUseResources[any(value)]
	if !found {
		// Resource not found in our tracking, just close it
		return p.factory.Close(value)
	}

	// Remove from inUseResources
	delete(p.inUseResources, any(value))
	p.stats.InUse--

	// Check if the resource is still valid
	if !p.factory.Validate(value) {
		return p.discardResourceLocked(res, true)
	}

	// Resource is valid, return it to the pool
	res.LastChecked = time.Now()
	p.resources = append(p.resources, res)
	p.stats.Available = len(p.resources)
	return nil
}

// Close shuts down the pool and releases all resources
func (p *SimplePool[T]) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.closed {
		return ErrPoolClosed
	}

	// Stop background health check
	p.cancel()
	p.closed = true

	var lastErr error

	// Close all resources in the available pool
	for _, res := range p.resources {
		if err := p.factory.Close(res.Value); err != nil {
			lastErr = err
		}
	}
	p.resources = nil

	// Close all in-use resources
	for _, res := range p.inUseResources {
		if err := p.factory.Close(res.Value); err != nil {
			lastErr = err
		}
	}
	p.inUseResources = nil

	// Clear stats
	p.stats = Stats{}

	return lastErr
}

// Stats returns current statistics about the pool
func (p *SimplePool[T]) Stats() Stats {
	p.mu.Lock()
	defer p.mu.Unlock()

	stats := p.stats
	stats.Available = len(p.resources)
	stats.InUse = len(p.inUseResources)
	return stats
}

// healthCheckLoop periodically checks the health of idle resources
func (p *SimplePool[T]) healthCheckLoop() {
	ticker := time.NewTicker(p.healthCheck)
	defer ticker.Stop()

	for {
		select {
		case <-p.ctx.Done():
			return
		case <-ticker.C:
			p.performHealthCheck()
		}
	}
}

// performHealthCheck validates all resources in the pool
func (p *SimplePool[T]) performHealthCheck() {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.closed {
		return
	}

	// Check resources in the pool
	validResources := make([]*Resource[T], 0, len(p.resources))
	for _, res := range p.resources {
		if p.factory.Validate(res.Value) {
			res.LastChecked = time.Now()
			validResources = append(validResources, res)
		} else {
			_ = p.discardResourceLocked(res, true) // true indicates validation failure
		}
	}

	p.resources = validResources
	p.stats.Available = len(p.resources)
}

// discardResource closes and removes a resource, returning its token to the semaphore
func (p *SimplePool[T]) discardResource(res *Resource[T], failed bool) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.discardResourceLocked(res, failed)
}

// discardResourceLocked is the lock-free version of discardResource
// caller must hold p.mu
// 'failed' parameter indicates if the discard is due to validation failure
func (p *SimplePool[T]) discardResourceLocked(res *Resource[T], failed bool) error {
	if res == nil {
		return nil
	}

	// Close the resource
	err := p.factory.Close(res.Value)

	// Update stats
	p.stats.Discarded++
	p.stats.CurrentSize--
	if failed {
		p.stats.Failed++
	}

	// Return token to semaphore
	select {
	case p.semaphore <- struct{}{}:
		// Successfully returned token
	default:
		// Semaphore is full, which shouldn't happen if our tracking is correct
		// This could indicate a bug in the pool implementation
	}

	return err
}
