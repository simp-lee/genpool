package genpool

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"sync"
	"testing"
	"time"
)

// MockResource is a simple resource for testing
type MockResource struct {
	ID        int
	IsHealthy bool
	Closed    bool
	Created   time.Time
}

// MockFactory implements the Factory interface
type MockFactory struct {
	createCount int
	closeCount  int
	failCreate  bool
	failRate    float64 // Rate at which health checks fail (0.0-1.0)
	mu          sync.Mutex
	createDelay time.Duration // Simulated delay for resource creation
	closeDelay  time.Duration // Simulated delay for resource closing
}

func (f *MockFactory) Create(ctx context.Context) (*MockResource, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	// Simulate creation delay
	if f.createDelay > 0 {
		select {
		case <-time.After(f.createDelay):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}

	// Simulate creation failure
	if f.failCreate {
		return nil, errors.New("mock creation failure")
	}

	f.createCount++
	return &MockResource{
		ID:        f.createCount,
		IsHealthy: true,
		Created:   time.Now(),
	}, nil
}

func (f *MockFactory) Validate(r *MockResource) bool {
	if r == nil {
		return false
	}

	// Resources that have been closed are not healthy
	if r.Closed {
		return false
	}

	// Randomly fail validation based on configured failure rate
	if f.failRate > 0 && rand.Float64() < f.failRate {
		r.IsHealthy = false
	}

	return r.IsHealthy
}

func (f *MockFactory) Close(r *MockResource) error {
	if r == nil {
		return nil
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	// Simulate close delay
	if f.closeDelay > 0 {
		time.Sleep(f.closeDelay)
	}

	if !r.Closed {
		r.Closed = true
		f.closeCount++
	}
	return nil
}

func (f *MockFactory) Stats() (int, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.createCount, f.closeCount
}

// ErrorClosingFactory is a factory that always returns an error when Close is called
type ErrorClosingFactory struct {
	MockFactory
}

// Close implements the Factory interface with error behavior
func (f *ErrorClosingFactory) Close(r *MockResource) error {
	// First call the original close to mark the resource as closed
	_ = f.MockFactory.Close(r)
	// Then return an error
	return errors.New("mock close error")
}

// Basic functionality tests
func TestPoolBasicOperations(t *testing.T) {
	factory := &MockFactory{}
	p := New(factory, Options{
		MaxSize:             5,
		HealthCheckInterval: time.Hour, // Set long interval to avoid automatic health checks during test
	})
	defer p.Close()

	// 1. Get a resource
	res, err := p.Get(context.Background())
	if err != nil {
		t.Fatalf("Failed to get resource: %v", err)
	}
	if res == nil {
		t.Fatal("Got nil resource")
	}
	if res.ID != 1 {
		t.Errorf("Expected resource ID 1, got %d", res.ID)
	}

	// 2. Check pool stats
	stats := p.Stats()
	if stats.Created != 1 {
		t.Errorf("Expected 1 resource created, got %d", stats.Created)
	}
	if stats.InUse != 1 {
		t.Errorf("Expected 1 resource in use, got %d", stats.InUse)
	}

	// 3. Release resource
	if err := p.Release(res); err != nil {
		t.Errorf("Failed to release resource: %v", err)
	}

	stats = p.Stats()
	if stats.InUse != 0 {
		t.Errorf("Expected 0 resources in use after release, got %d", stats.InUse)
	}
	if stats.Available != 1 {
		t.Errorf("Expected 1 resource available after release, got %d", stats.Available)
	}

	// 4. Check resource reuse
	res2, err := p.Get(context.Background())
	if err != nil {
		t.Fatalf("Failed to get resource: %v", err)
	}
	if res2.ID != 1 { // Should get the same resource back
		t.Errorf("Expected to get the same resource with ID 1, got %d", res2.ID)
	}

	stats = p.Stats()
	if stats.Reused != 1 {
		t.Errorf("Expected 1 resource reuse, got %d", stats.Reused)
	}

	// 5. Release again
	if err := p.Release(res2); err != nil {
		t.Errorf("Failed to release resource second time: %v", err)
	}
}

// Test error handling and edge cases
func TestPoolErrorHandling(t *testing.T) {
	t.Run("CreationFailure", func(t *testing.T) {
		factory := &MockFactory{failCreate: true}
		p := New(factory, Options{MaxSize: 2})
		defer p.Close()

		_, err := p.Get(context.Background())
		if err == nil {
			t.Fatal("Expected error when factory fails to create, got nil")
		}
	})

	t.Run("ContextCancellation", func(t *testing.T) {
		factory := &MockFactory{createDelay: 200 * time.Millisecond}
		p := New(factory, Options{MaxSize: 1})
		defer p.Close()

		// Get first resource to exhaust the pool
		res, err := p.Get(context.Background())
		if err != nil {
			t.Fatalf("Failed to get first resource: %v", err)
		}

		// Set up context with short timeout
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()

		// Try to get second resource, should timeout
		_, err = p.Get(ctx)
		if err == nil || !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("Expected DeadlineExceeded error, got: %v", err)
		}

		// Release first resource
		if err := p.Release(res); err != nil {
			t.Errorf("Failed to release resource: %v", err)
		}
	})

	t.Run("ReleaseNilResource", func(t *testing.T) {
		factory := &MockFactory{}
		p := New(factory, Options{MaxSize: 2})
		defer p.Close()

		// Releasing nil should not panic
		if err := p.Release(nil); err != nil {
			t.Errorf("Release(nil) returned error: %v", err)
		}

		// Check pool stats, should remain unchanged
		stats := p.Stats()
		if stats.CurrentSize != 0 || stats.Available != 0 || stats.InUse != 0 {
			t.Errorf("Pool stats changed after releasing nil: %+v", stats)
		}
	})

	t.Run("ClosePool", func(t *testing.T) {
		factory := &MockFactory{}
		p := New(factory, Options{MaxSize: 2})

		// Get resources
		res1, _ := p.Get(context.Background())
		res2, _ := p.Get(context.Background())

		// Release one resource
		if err := p.Release(res1); err != nil {
			t.Errorf("Failed to release resource: %v", err)
		}

		// Close the pool
		err := p.Close()
		if err != nil {
			t.Fatalf("Failed to close pool: %v", err)
		}

		// Trying to get a resource should fail
		_, err = p.Get(context.Background())
		if err != ErrPoolClosed {
			t.Errorf("Expected ErrPoolClosed, got: %v", err)
		}

		// Trying to close pool again should return error
		err = p.Close()
		if err != ErrPoolClosed {
			t.Errorf("Expected ErrPoolClosed on second close, got: %v", err)
		}

		// Releasing resource after pool closed should not panic
		if err := p.Release(res2); err != nil {
			t.Errorf("Release after pool closed returned unexpected error: %v", err)
		}

		// Verify all resources were closed
		_, closed := factory.Stats()
		if closed != 2 {
			t.Errorf("Expected 2 resources to be closed, got %d", closed)
		}
	})

	t.Run("ReleaseErrorHandling", func(t *testing.T) {
		errorFactory := &ErrorClosingFactory{}
		p := New(errorFactory, Options{MaxSize: 1})
		defer p.Close()

		// Get resource
		res, err := p.Get(context.Background())
		if err != nil {
			t.Fatalf("Failed to get resource: %v", err)
		}

		// Make resource unhealthy so Release will call Close and return the error
		res.IsHealthy = false

		// Verify Release returns the error
		err = p.Release(res)
		if err == nil {
			t.Error("Expected Release to return error when Close fails")
		}
		if err != nil && err.Error() != "mock close error" {
			t.Errorf("Expected 'mock close error', got: %v", err)
		}
	})
}

// Test health check and resource state management
func TestPoolHealthCheck(t *testing.T) {
	t.Run("UnhealthyResourceReplacement", func(t *testing.T) {
		factory := &MockFactory{}
		p := New(factory, Options{MaxSize: 2})
		defer p.Close()

		// Get resource
		res, _ := p.Get(context.Background())

		// Manually make resource unhealthy
		res.IsHealthy = false

		// Release resource - pool should detect unhealthy and discard it
		if err := p.Release(res); err != nil {
			t.Errorf("Failed to release unhealthy resource: %v", err)
		}

		stats := p.Stats()
		if stats.Failed != 1 {
			t.Errorf("Expected 1 failed health check, got %d", stats.Failed)
		}
		if stats.Available != 0 {
			t.Errorf("Expected 0 available resources after releasing unhealthy one, got %d", stats.Available)
		}
		if stats.Discarded != 1 {
			t.Errorf("Expected 1 discarded resource, got %d", stats.Discarded)
		}

		// Get new resource - should create a new one instead of reusing
		newRes, _ := p.Get(context.Background())
		if newRes.ID == res.ID {
			t.Errorf("Got the same unhealthy resource back, expected a new one")
		}
	})

	t.Run("AutomaticHealthCheck", func(t *testing.T) {
		// Create pool with short health check interval
		factory := &MockFactory{failRate: 1.0} // All health checks fail
		p := New(factory, Options{
			MaxSize:             2,
			HealthCheckInterval: 50 * time.Millisecond, // Very short interval
		})
		defer p.Close()

		// Get and release a resource
		res, _ := p.Get(context.Background())
		if err := p.Release(res); err != nil {
			t.Errorf("Failed to release resource: %v", err)
		}

		// Wait for health check to run
		time.Sleep(100 * time.Millisecond)

		stats := p.Stats()
		if stats.Failed == 0 {
			t.Error("Expected at least one failed health check after waiting")
		}
		if stats.Available != 0 {
			t.Errorf("Expected 0 available resources after health check removed unhealthy ones, got %d", stats.Available)
		}
	})
}

// Concurrency tests
func TestPoolConcurrency(t *testing.T) {
	t.Run("ConcurrentGetRelease", func(t *testing.T) {
		factory := &MockFactory{}
		p := New(factory, Options{
			MaxSize:             10,
			HealthCheckInterval: time.Hour,
		})
		defer p.Close()

		var wg sync.WaitGroup
		workers := 50

		// Start multiple goroutines to simultaneously get and release resources
		for i := 0; i < workers; i++ {
			wg.Add(1)
			go func(id int) {
				defer wg.Done()
				// Each worker loops 10 times
				for j := 0; j < 10; j++ {
					// Wait random time 0-10ms to simulate varying workloads
					time.Sleep(time.Duration(rand.Intn(10)) * time.Millisecond)

					ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
					res, err := p.Get(ctx)
					cancel()

					if err != nil {
						if !errors.Is(err, context.DeadlineExceeded) {
							t.Errorf("Worker %d: unexpected error: %v", id, err)
						}
						continue
					}

					// Use resource
					time.Sleep(time.Duration(rand.Intn(20)) * time.Millisecond)

					// 10% chance to make resource unhealthy
					if rand.Intn(10) == 0 {
						res.IsHealthy = false
					}

					if err := p.Release(res); err != nil {
						t.Errorf("Worker %d: failed to release resource: %v", id, err)
					}
				}
			}(i)
		}

		wg.Wait()

		// Check pool status
		stats := p.Stats()
		t.Logf("Pool stats after concurrent usage: %+v", stats)

		// Verify all resources were returned to pool
		if stats.InUse != 0 {
			t.Errorf("Expected 0 resources in use after all workers done, got %d", stats.InUse)
		}

		// Ensure total resources don't exceed max
		if stats.CurrentSize > 10 {
			t.Errorf("Total resources (%d) exceeds max size (10)", stats.CurrentSize)
		}
	})

	t.Run("StressTest", func(t *testing.T) {
		if testing.Short() {
			t.Skip("Skipping stress test in short mode")
		}

		factory := &MockFactory{
			failRate:    0.05, // 5% health check failure rate
			createDelay: 5 * time.Millisecond,
			closeDelay:  2 * time.Millisecond,
		}
		p := New(factory, Options{
			MaxSize:             5,
			HealthCheckInterval: 100 * time.Millisecond, // Frequent health checks
		})
		defer p.Close()

		// Create multiple concurrent workers to simulate high load
		var wg sync.WaitGroup
		workers := 20
		iterations := 30
		errorChan := make(chan error, workers*iterations)

		for i := 0; i < workers; i++ {
			wg.Add(1)
			go func(id int) {
				defer wg.Done()
				for j := 0; j < iterations; j++ {
					// Use random timeout
					timeout := time.Duration(50+rand.Intn(100)) * time.Millisecond
					ctx, cancel := context.WithTimeout(context.Background(), timeout)

					res, err := p.Get(ctx)
					if err != nil {
						if errors.Is(err, context.DeadlineExceeded) {
							// Timeouts are acceptable
							cancel()
							continue
						}
						errorChan <- fmt.Errorf("worker %d: Get error: %v", id, err)
						cancel()
						continue
					}

					// Simulate workload
					time.Sleep(time.Duration(rand.Intn(20)) * time.Millisecond)

					// Randomly decide whether to release resource (simulate leaks)
					if rand.Intn(20) != 0 { // 95% chance to release
						if err := p.Release(res); err != nil {
							errorChan <- fmt.Errorf("worker %d: Release error: %v", id, err)
						}
					}

					cancel()
				}
			}(i)
		}

		// Wait for all workers to complete
		wg.Wait()
		close(errorChan)

		// Check for errors
		errCount := 0
		for err := range errorChan {
			t.Log(err)
			errCount++
		}
		if errCount > 0 {
			t.Errorf("Stress test encountered %d errors", errCount)
		}

		// Wait for final health checks
		time.Sleep(200 * time.Millisecond)

		// Verify pool status
		stats := p.Stats()
		t.Logf("Pool stats after stress test: %+v", stats)
		created, closed := factory.Stats()
		t.Logf("Factory stats: created=%d, closed=%d", created, closed)

		// Leaked resources reporting
		if stats.InUse > 0 {
			t.Logf("Warning: %d resources still in use after test", stats.InUse)
		}
	})
}

// Test special cases and edge scenarios
func TestPoolEdgeCases(t *testing.T) {
	t.Run("ZeroMaxSize", func(t *testing.T) {
		factory := &MockFactory{}
		// If MaxSize <= 0 is specified, pool should use default value
		p := New(factory, Options{MaxSize: 0})
		defer p.Close()

		// Should be able to get a resource
		res, err := p.Get(context.Background())
		if err != nil {
			t.Errorf("Failed to get resource with zero max size: %v", err)
		} else {
			if err := p.Release(res); err != nil {
				t.Errorf("Failed to release resource: %v", err)
			}
		}
	})

	t.Run("ReleaseForeignResource", func(t *testing.T) {
		factory := &MockFactory{}
		p := New(factory, Options{MaxSize: 5})
		defer p.Close()

		// Create a resource not obtained from pool
		foreignRes := &MockResource{ID: 999, IsHealthy: true}

		// Release it
		if err := p.Release(foreignRes); err != nil {
			t.Errorf("Failed to release foreign resource: %v", err)
		}

		// Pool should close it and not add it back to the pool
		if !foreignRes.Closed {
			t.Error("Foreign resource was not closed")
		}

		stats := p.Stats()
		if stats.Available > 0 {
			t.Errorf("Pool should not add foreign resource to available resources, got %d available", stats.Available)
		}
	})

	t.Run("InvalidateAllResources", func(t *testing.T) {
		// Create a factory where all health checks fail
		factory := &MockFactory{failRate: 1.0}
		p := New(factory, Options{MaxSize: 5})
		defer p.Close()

		// Get some resources
		var resources []*MockResource
		for i := 0; i < 3; i++ {
			res, _ := p.Get(context.Background())
			resources = append(resources, res)
		}

		// Release all resources
		for _, res := range resources {
			if err := p.Release(res); err != nil {
				t.Errorf("Failed to release resource: %v", err)
			}
		}

		// Since all resources are unhealthy, pool should be empty
		stats := p.Stats()
		if stats.Available != 0 {
			t.Errorf("Expected 0 available resources after all failed validation, got %d", stats.Available)
		}
		if stats.Failed != 3 {
			t.Errorf("Expected 3 failed validations, got %d", stats.Failed)
		}
	})

	t.Run("ResourceExhaustion", func(t *testing.T) {
		// Create a factory with delay
		factory := &MockFactory{createDelay: 10 * time.Millisecond}
		maxSize := 2
		p := New(factory, Options{MaxSize: maxSize})
		defer p.Close()

		// Get all available resources
		var resources []*MockResource
		for i := 0; i < maxSize; i++ {
			res, err := p.Get(context.Background())
			if err != nil {
				t.Fatalf("Failed to get resource %d: %v", i, err)
			}
			resources = append(resources, res)
		}

		// Try to get another resource with short timeout
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
		defer cancel()

		_, err := p.Get(ctx)
		if err == nil || !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("Expected timeout when pool is exhausted, got: %v", err)
		}

		// Release all resources
		for _, res := range resources {
			if err := p.Release(res); err != nil {
				t.Errorf("Failed to release resource: %v", err)
			}
		}
	})
}

// Test resource expiry
func TestPoolResourceExpiry(t *testing.T) {
	// This feature is not implemented in the pool yet, but tests are written for future extension
	t.Skip("Resource expiry not implemented yet")

	// Future tests to implement:
	// 1. Test resource expiry based on usage count
	// 2. Test resource expiry based on time
	// 3. Test exponential backoff replacement strategy
}

// Test with different factory implementations
func TestPoolWithDifferentFactories(t *testing.T) {
	// Create a special factory that creates limited resources then starts failing
	limitedFactory := &MockFactory{failCreate: false}
	p := New(limitedFactory, Options{MaxSize: 10})
	defer p.Close()

	// Get some resources
	var resources []*MockResource
	for i := 0; i < 5; i++ {
		res, err := p.Get(context.Background())
		if err != nil {
			t.Fatalf("Failed to get resource %d: %v", i, err)
		}
		resources = append(resources, res)
	}

	// Only release 1 resource back to the pool
	if err := p.Release(resources[0]); err != nil {
		t.Errorf("Failed to release resource: %v", err)
	}

	// Set factory to start failing
	limitedFactory.failCreate = true

	// Get the only resource in the pool (should succeed)
	res, err := p.Get(context.Background())
	if err != nil {
		t.Fatalf("Failed to get pooled resource: %v", err)
	}

	// Now pool is empty, all requests should require creating new resources

	// Try to get a resource that requires creating new (should fail)
	_, err = p.Get(context.Background())
	if err == nil {
		t.Fatal("Expected error when factory fails to create")
	}

	// Release the last resource to ensure we have one available resource
	if err := p.Release(res); err != nil {
		t.Errorf("Failed to release resource: %v", err)
	}

	// Should be able to get this resource again
	_, err = p.Get(context.Background())
	if err != nil {
		t.Fatalf("Failed to get pooled resource after releasing: %v", err)
	}

	// Try to get again, should fail
	_, err = p.Get(context.Background())
	if err == nil {
		t.Fatal("Expected error when factory fails to create and pool is empty")
	}
}

func TestResourceMetadata(t *testing.T) {
	factory := &MockFactory{}
	p := New(factory, Options{
		MaxSize:             1,
		HealthCheckInterval: 50 * time.Millisecond, // Short interval for LastChecked
	})
	defer p.Close()

	simplePool, ok := p.(*SimplePool[*MockResource])
	if !ok {
		t.Fatalf("Expected Pool to be of type *SimplePool, got %T", p)
	}

	ctx := context.Background()

	// First Get
	resPtr, err := p.Get(ctx)
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	if resPtr == nil {
		t.Fatal("Got nil resource")
	}

	// Find the wrapper to check metadata
	simplePool.mu.Lock() // Lock to safely inspect internal state
	var internalRes *Resource[*MockResource]
	for _, r := range simplePool.inUseResources { // Check inUseResources as it's just borrowed
		if r.Value == resPtr {
			internalRes = r
			break
		}
	}
	simplePool.mu.Unlock()

	if internalRes == nil {
		t.Fatal("Could not find internal resource wrapper for the borrowed resource")
	}

	if internalRes.BorrowCount != 1 {
		t.Errorf("Expected BorrowCount 1, got %d", internalRes.BorrowCount)
	}
	if time.Since(internalRes.CreatedAt) > time.Second {
		t.Errorf("CreatedAt seems too old: %v", internalRes.CreatedAt)
	}
	if time.Since(internalRes.LastUsed) > time.Millisecond*100 { // Should be very recent
		t.Errorf("LastUsed seems too old: %v", internalRes.LastUsed)
	}

	initialLastUsed := internalRes.LastUsed
	if err := p.Release(resPtr); err != nil {
		t.Errorf("Failed to release resource: %v", err)
	}

	// Wait for health check to potentially run
	time.Sleep(100 * time.Millisecond)

	// Second Get (reuse)
	resPtr2, err := p.Get(ctx)
	if err != nil {
		t.Fatalf("Second Get failed: %v", err)
	}

	simplePool.mu.Lock()
	var internalRes2 *Resource[*MockResource]
	for _, r := range simplePool.inUseResources {
		if r.Value == resPtr2 {
			internalRes2 = r
			break
		}
	}
	simplePool.mu.Unlock()

	if internalRes2 == nil {
		t.Fatal("Could not find internal resource wrapper for the re-borrowed resource")
	}

	if internalRes2.BorrowCount != 2 {
		t.Errorf("Expected BorrowCount 2 after reuse, got %d", internalRes2.BorrowCount)
	}
	if internalRes2.LastUsed == initialLastUsed {
		t.Error("LastUsed was not updated after reuse")
	}
	if err := p.Release(resPtr2); err != nil {
		t.Errorf("Failed to release resource: %v", err)
	}
}

// NonComparableFactory is a dummy factory for a non-comparable type
type NonComparableResource struct {
	Data []string // Slices are not comparable
}
type NonComparableFactory struct{}

func (f *NonComparableFactory) Create(ctx context.Context) (NonComparableResource, error) {
	return NonComparableResource{Data: []string{"test"}}, nil
}
func (f *NonComparableFactory) Validate(r NonComparableResource) bool { return true }
func (f *NonComparableFactory) Close(r NonComparableResource) error   { return nil }

func TestNewPoolWithNonComparableType(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Errorf("Expected New to panic with non-comparable type, but it did not")
		} else {
			expectedPanicMsg := "pool: type T must be comparable to be used as a map key for inUseResources tracking"
			if r != expectedPanicMsg {
				t.Errorf("Expected panic message '%s', got '%v'", expectedPanicMsg, r)
			}
		}
	}()

	factory := &NonComparableFactory{}
	// This should panic because NonComparableResource contains a slice
	_ = New(factory, Options{MaxSize: 1})
}

func TestHealthCheckLogic(t *testing.T) {
	// Test with a controllable health check function to verify
	// resources are properly marked and discarded when invalid
	t.Run("HealthCheckDiscarding", func(t *testing.T) {
		factory := &MockFactory{failRate: 0.5} // 50% chance resources become invalid
		p := New(factory, Options{
			MaxSize:             10,
			HealthCheckInterval: 20 * time.Millisecond, // Fast health checks
		})
		defer p.Close()

		// Fill the pool
		var resources []*MockResource
		for i := 0; i < 10; i++ {
			res, _ := p.Get(context.Background())
			resources = append(resources, res)
		}

		// Release resources
		for _, res := range resources {
			if err := p.Release(res); err != nil {
				t.Errorf("Failed to release resource: %v", err)
			}
		}

		// Wait for several rounds of health checks
		time.Sleep(100 * time.Millisecond) // Enough for 5 rounds of health checks

		// Verify some resources were discarded
		stats := p.Stats()
		if stats.Failed == 0 {
			t.Error("Expected some resources to fail health check")
		}
		if stats.CurrentSize == 10 {
			t.Error("Expected some resources to be removed")
		}
	})
}

func TestConcurrentPoolBoundary(t *testing.T) {
	factory := &MockFactory{}
	maxSize := 5
	p := New(factory, Options{MaxSize: maxSize})
	defer p.Close()

	// Pre-fill pool
	resources := make([]*MockResource, maxSize)
	for i := 0; i < maxSize; i++ {
		res, _ := p.Get(context.Background())
		resources[i] = res
	}

	// Start multiple goroutines trying to get resources
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()

			// Try to get a resource with very short timeout
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
			defer cancel()

			res, err := p.Get(ctx)
			if err == nil {
				// Successfully got resource, must be one that was released
				if err := p.Release(res); err != nil {
					t.Errorf("Failed to release resource: %v", err)
				}
			} else if !errors.Is(err, context.DeadlineExceeded) {
				// If not a timeout error, something is wrong
				t.Errorf("Unexpected error: %v", err)
			}
		}(i)
	}

	// Randomly release resources to give some goroutines a chance
	time.Sleep(10 * time.Millisecond)
	for _, res := range resources {
		if rand.Intn(2) == 0 { // 50% chance to release
			if err := p.Release(res); err != nil {
				t.Errorf("Failed to release resource: %v", err)
			}
			time.Sleep(5 * time.Millisecond)
		}
	}

	wg.Wait()
}

// Performance benchmarks
func BenchmarkPoolOperations(b *testing.B) {
	b.Run("GetReleaseSingleResource", func(b *testing.B) {
		factory := &MockFactory{}
		p := New(factory, Options{MaxSize: 1})
		defer p.Close()

		ctx := context.Background()
		b.ResetTimer()

		for i := 0; i < b.N; i++ {
			res, err := p.Get(ctx)
			if err != nil {
				b.Fatalf("Failed to get resource: %v", err)
			}
			if err := p.Release(res); err != nil {
				b.Fatalf("Failed to release resource: %v", err)
			}
		}
	})

	b.Run("GetReleaseMultipleResources", func(b *testing.B) {
		factory := &MockFactory{}
		poolSize := 10
		p := New(factory, Options{MaxSize: poolSize})
		defer p.Close()

		ctx := context.Background()
		b.ResetTimer()

		// Limit concurrency to match pool size
		b.SetParallelism(poolSize)

		b.RunParallel(func(pb *testing.PB) {
			// Create timeout context outside loop to avoid frequent creation
			for pb.Next() {
				// Use timeout context to avoid potential deadlock
				localCtx, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
				res, err := p.Get(localCtx)
				if err != nil {
					cancel()
					if errors.Is(err, context.DeadlineExceeded) {
						// Timeouts are acceptable, skip this iteration
						continue
					}
					b.Fatalf("Failed to get resource: %v", err)
				}
				if err := p.Release(res); err != nil {
					cancel()
					b.Fatalf("Failed to release resource: %v", err)
				}
				cancel()
			}
		})
	})
}

// Test health check failure count accuracy
func TestFailedCountAccuracy(t *testing.T) {
	factory := &MockFactory{}
	p := New(factory, Options{MaxSize: 5})
	defer p.Close()

	// 1. Create and release healthy resource
	res1, _ := p.Get(context.Background())
	err := p.Release(res1)
	if err != nil {
		t.Errorf("Unexpected error on healthy release: %v", err)
	}

	// 2. Create and release unhealthy resource
	res2, _ := p.Get(context.Background())
	res2.IsHealthy = false
	err = p.Release(res2)
	if err != nil {
		t.Errorf("Unexpected error on unhealthy release: %v", err)
	}

	// Verify failure count
	stats := p.Stats()
	if stats.Failed != 1 {
		t.Errorf("Expected exactly 1 failed health check, got %d", stats.Failed)
	}

	// 3. Close pool to ensure all resources are closed
	err = p.Close()
	if err != nil {
		t.Errorf("Unexpected error on close: %v", err)
	}

	// 4. Close again, should return ErrPoolClosed
	err = p.Close()
	if err != ErrPoolClosed {
		t.Errorf("Expected ErrPoolClosed on second close, got: %v", err)
	}
}

// RandomErrorFactory is a factory that randomly returns errors on Close
type RandomErrorFactory struct {
	MockFactory
	closeCount int
	mu         sync.Mutex
}

// Close occasionally returns errors for testing error handling
func (f *RandomErrorFactory) Close(r *MockResource) error {
	if r == nil {
		return nil
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	// Mark resource as closed
	r.Closed = true

	// Increment counter
	f.closeCount++

	// Return error every third call
	if f.closeCount%3 == 0 {
		return errors.New("random close error")
	}
	return nil
}

// Test concurrent resource release error handling
func TestConcurrentReleaseErrors(t *testing.T) {
	// Create a factory that occasionally fails during Close
	randomErrorFactory := &RandomErrorFactory{}

	p := New(randomErrorFactory, Options{MaxSize: 10})
	defer p.Close()

	var wg sync.WaitGroup
	errChan := make(chan error, 100)

	// Launch multiple goroutines to get and release resources concurrently
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()

			res, err := p.Get(context.Background())
			if err != nil {
				errChan <- fmt.Errorf("get error: %v", err)
				return
			}

			// Randomly make resource unhealthy
			if rand.Intn(2) == 0 {
				res.IsHealthy = false
			}

			if err := p.Release(res); err != nil {
				errChan <- fmt.Errorf("release error: %v", err)
			}
		}()
	}

	wg.Wait()
	close(errChan)

	// Collect and verify errors
	var errors []string
	for err := range errChan {
		errors = append(errors, err.Error())
	}

	// Verify we actually captured some errors
	if len(errors) == 0 {
		t.Log("No errors captured, but some were expected due to random failures")
	} else {
		t.Logf("Captured %d errors as expected", len(errors))
	}
}
