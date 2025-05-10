# GenPool - Go Generic Resource Pool

[English](#introduction) | [简体中文](#简介)

## Introduction

`GenPool` is a generic resource pool library implemented using Go's generics feature, capable of efficiently managing any type of reusable resources. It provides important features such as thread safety, health checking, and resource limiting.

## Features

- **Type Safety**: Implemented using Go 1.18+ generics with no type assertion or reflection overhead
- **Resource Health Checking**: Automatically validates idle resources periodically
- **Concurrent Safety**: Supports concurrent operations across multiple goroutines
- **Resource Limiting**: Prevents resource leaks and over-allocation
- **Complete Statistics**: Provides detailed resource pool usage information
- **Flexible Configuration**: Supports custom resource factories and pool settings

## Installation

```bash
go get github.com/simp-lee/genpool
```

Requires Go 1.18 or higher.

## Quick Start

### 1. Define Resource and Factory

First, define your resource type and implement the `Factory` interface to manage the resource lifecycle:

```go
package main

import (
    "context"
    "database/sql"
    "time"
    
    "github.com/simp-lee/genpool"
)

// Define resource factory
type DBConnectionFactory struct {
    connectionString string
}

// Create implements resource creation
func (f *DBConnectionFactory) Create(ctx context.Context) (*sql.DB, error) {
    return sql.Open("mysql", f.connectionString)
}

// Validate checks if connection is healthy
func (f *DBConnectionFactory) Validate(db *sql.DB) bool {
    return db.PingContext(context.Background()) == nil
}

// Close closes the connection
func (f *DBConnectionFactory) Close(db *sql.DB) error {
    return db.Close()
}
```

### 2. Create and Use Resource Pool

Then, create and use the resource pool:

```go
func main() {
    // Create resource factory
    factory := &DBConnectionFactory{
        connectionString: "user:password@tcp(localhost:3306)/dbname",
    }
    
    // Create resource pool
    pool := genpool.New(factory, genpool.Options{
        MaxSize:             10,            // Maximum number of resources
        HealthCheckInterval: time.Minute,   // Health check interval
    })
    defer pool.Close()
    
    // Get resource
    ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
    defer cancel()
    
    conn, err := pool.Get(ctx)
    if err != nil {
        // Handle error
        return
    }
    
    // Use resource
    // ...
    
    // Return resource to pool
    if err := pool.Release(conn); err != nil {
        // Handle resource release error
    }
    
    // Get pool statistics
    stats := pool.Stats()
    fmt.Printf("Pool status: %+v\n", stats)
}
```

## API Documentation

### Core Interfaces

#### Factory[T any]

Resource factory interface, defining resource lifecycle management:

```go
type Factory[T any] interface {
    // Create creates a new resource
    Create(ctx context.Context) (T, error)
    
    // Validate checks if a resource is valid/healthy
    Validate(resource T) bool
    
    // Close cleans up a resource
    Close(resource T) error
}
```

#### Pool[T any]

Resource pool interface, providing core pool operations:

```go
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
```

### Configuration Options

```go
type Options struct {
    // Maximum number of resources
    MaxSize int
    
    // Health check interval for idle resources
    HealthCheckInterval time.Duration
}
```

### Statistics

The pool provides the following statistics:

```go
type Stats struct {
    Created     int64 // Total number of resources created
    Reused      int64 // Number of times resources were reused
    Failed      int64 // Number of failed health checks
    Discarded   int64 // Number of discarded resources
    CurrentSize int   // Current number of resources managed by pool
    Available   int   // Number of resources currently available in the pool
    InUse       int   // Number of resources currently borrowed from the pool
}
```

## Advanced Usage

### Custom Health Check Strategies

You can implement custom resource validation logic based on various factors:

```go
func (f *MyResourceFactory) Validate(res *MyResource) bool {
    // Check for timeout
    if time.Since(res.lastUsed) > 30*time.Minute {
        return false
    }
    
    // Check error count
    if res.errorCount > 5 {
        return false
    }
    
    // Perform actual health check
    return res.Ping() == nil
}
```

### Handling Resource Creation Delays

For resources that might take time to create, use context timeout:

```go
ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
defer cancel()

res, err := pool.Get(ctx)
if err != nil {
    if errors.Is(err, context.DeadlineExceeded) {
        // Resource creation timed out
    }
    // Handle other errors
}
```

### Pool Shutdown and Resource Cleanup

Properly closing the pool ensures all resources are cleaned up:

```go
func shutdown() {
    // Close pool and check for errors
    if err := pool.Close(); err != nil {
        log.Printf("Error closing resource pool: %v", err)
    }
}
```

## Best Practices

1. **Adjust Pool Size Appropriately**: Set reasonable pool size based on load and resource cost
2. **Set Reasonable Health Check Intervals**: Too frequent adds CPU overhead, too rare might lead to using invalid resources
3. **Always Handle `Release` Return Errors**: They may contain important resource cleanup issues
4. **Define Precise Validation Logic**: Ensure the `Validate` method accurately detects resource health
5. **Use Context to Manage Timeouts**: Prevent deadlocks and resource exhaustion under high load

## Thread Safety

`GenPool` is completely thread-safe and can be safely used across multiple goroutines. It uses mutex locks and semaphore mechanisms internally to coordinate access to shared resources.

## Performance Considerations

- Resource pool uses a LIFO (Last In, First Out) strategy for better cache locality
- Health checks are performed in a separate goroutine and won't block main operations
- Semaphore pattern is used to limit concurrent resource creation

## Error Handling

GenPool may return the following predefined errors:

- `ErrPoolClosed`: Pool is closed
- `ErrPoolFull`: Pool is at capacity limit
- `ErrTimeout`: Operation timed out

Additionally, errors from the resource factory may be returned from `Get` and `Release` methods.

## Contributing

Contributions are welcome! Feel free to submit issues or pull requests.

## License

MIT License

---

# GenPool - Go泛型资源池

[English](#introduction) | [简体中文](#简介)

## 简介

`GenPool` 是一个使用 Go 泛型特性实现的通用资源池库，能够高效管理任何类型的可重用资源。它提供了线程安全、健康检查、资源限制等重要功能。

## 功能特性

- **类型安全**：使用 Go 1.18+ 的泛型实现，没有类型断言或反射开销
- **资源健康检查**：自动定期验证空闲资源
- **并发安全**：支持多 goroutine 并发操作
- **资源限制**：防止资源泄露和过度分配
- **完整统计信息**：提供资源池使用详情
- **灵活配置**：支持自定义资源工厂和池配置

## 安装

```bash
go get github.com/simp-lee/genpool
```

要求 Go 1.18 或更高版本。

## 快速开始

### 1. 定义资源和工厂

首先，定义您的资源类型和实现 `Factory` 接口来管理资源的生命周期：

```go
package main

import (
    "context"
    "database/sql"
    "time"
    
    "github.com/simp-lee/genpool"
)

// 定义资源工厂
type DBConnectionFactory struct {
    connectionString string
}

// Create 实现资源创建
func (f *DBConnectionFactory) Create(ctx context.Context) (*sql.DB, error) {
    return sql.Open("mysql", f.connectionString)
}

// Validate 检查连接是否健康
func (f *DBConnectionFactory) Validate(db *sql.DB) bool {
    return db.PingContext(context.Background()) == nil
}

// Close 关闭连接
func (f *DBConnectionFactory) Close(db *sql.DB) error {
    return db.Close()
}
```

### 2. 创建并使用资源池

然后，创建并使用资源池：

```go
func main() {
    // 创建资源工厂
    factory := &DBConnectionFactory{
        connectionString: "user:password@tcp(localhost:3306)/dbname",
    }
    
    // 创建资源池
    pool := genpool.New(factory, genpool.Options{
        MaxSize:             10,            // 最大资源数
        HealthCheckInterval: time.Minute,   // 健康检查间隔
    })
    defer pool.Close()
    
    // 获取资源
    ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
    defer cancel()
    
    conn, err := pool.Get(ctx)
    if err != nil {
        // 处理错误
        return
    }
    
    // 使用资源
    // ...
    
    // 返回资源到池
    if err := pool.Release(conn); err != nil {
        // 处理资源释放错误
    }
    
    // 获取资源池统计信息
    stats := pool.Stats()
    fmt.Printf("资源池状态: %+v\n", stats)
}
```

## API 文档

### 核心接口

#### Factory[T any]

资源工厂接口，定义资源的生命周期管理：

```go
type Factory[T any] interface {
    // Create 创建一个新资源
    Create(ctx context.Context) (T, error)
    
    // Validate 检查资源是否有效/健康
    Validate(resource T) bool
    
    // Close 关闭资源
    Close(resource T) error
}
```

#### Pool[T any]

资源池接口，提供资源池的核心操作：

```go
type Pool[T any] interface {
    // Get 从池中获取资源，如果没有可用资源则创建新资源
    Get(ctx context.Context) (T, error)
    
    // Release 将资源返回到池中
    Release(value T) error
    
    // Close 关闭池并释放所有资源
    Close() error
    
    // Stats 返回当前池的统计信息
    Stats() Stats
}
```

### 配置选项

```go
type Options struct {
    // 最大资源数
    MaxSize int
    
    // 空闲资源的健康检查间隔
    HealthCheckInterval time.Duration
}
```

### 统计信息

池提供了以下统计信息：

```go
type Stats struct {
    Created     int64 // 创建的资源总数
    Reused      int64 // 资源被重用的次数
    Failed      int64 // 健康检查失败次数
    Discarded   int64 // 丢弃的资源数量
    CurrentSize int   // 当前由池管理的资源数
    Available   int   // 当前可用的资源数
    InUse       int   // 当前从池中借出的资源数
}
```

## 高级用法

### 定制健康检查策略

您可以基于多种因素实现自定义的资源验证逻辑：

```go
func (f *MyResourceFactory) Validate(res *MyResource) bool {
    // 检查超时
    if time.Since(res.lastUsed) > 30*time.Minute {
        return false
    }
    
    // 检查错误计数
    if res.errorCount > 5 {
        return false
    }
    
    // 执行实际健康检查
    return res.Ping() == nil
}
```

### 处理资源创建延迟

对于创建可能耗时的资源，可以使用上下文超时：

```go
ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
defer cancel()

res, err := pool.Get(ctx)
if err != nil {
    if errors.Is(err, context.DeadlineExceeded) {
        // 资源创建超时
    }
    // 处理其他错误
}
```

### 池关闭和资源清理

正确关闭池可确保所有资源得到清理：

```go
func shutdown() {
    // 关闭池并检查错误
    if err := pool.Close(); err != nil {
        log.Printf("关闭资源池时出错: %v", err)
    }
}
```

## 最佳实践

1. **适当调整池大小**：根据负载和资源成本设置合理的池大小
2. **设置合理的健康检查间隔**：太频繁会增加CPU开销，太少可能导致使用失效资源
3. **总是处理 `Release` 返回的错误**：它可能包含重要的资源清理问题
4. **定义精确的验证逻辑**：确保 `Validate` 方法能准确检测资源的健康状态
5. **使用上下文管理超时**：在高负载下防止死锁和资源耗尽

## 线程安全性

GenPool 是完全线程安全的，可以在多个 goroutine 之间安全使用。内部使用互斥锁和信号量机制来协调对共享资源的访问。

## 性能考虑

- 资源池使用 LIFO（后进先出）策略，从而获得更好的缓存局部性
- 健康检查在单独的 goroutine 中进行，不会阻塞主操作
- 信号量模式用于限制并发资源创建

## 错误处理

`GenPool` 可能返回以下预定义错误：

- `ErrPoolClosed`: 池已关闭
- `ErrPoolFull`: 池已达到容量上限
- `ErrTimeout`: 操作超时

此外，从 `Get` 和 `Release` 方法可能返回源自资源工厂的错误。

## 贡献

欢迎贡献！请随时提交问题或拉取请求。

## 许可证

MIT 许可证