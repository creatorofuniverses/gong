package topics

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/creatorofuniverses/gong/internal/telegram"
)

const resolverTestTimeout = 2 * time.Second

func TestResolverSharesOneCreationAndCachesExactKey(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int32
	resolver := NewResolver(context.Background(), 10, time.Second, func(_ context.Context, chatID, name string) (telegram.Topic, error) {
		if chatID != "-1001" || name != "Deploy" {
			t.Errorf("create arguments = %q, %q; want exact key", chatID, name)
		}
		if calls.Add(1) == 1 {
			close(started)
		}
		<-release
		return telegram.Topic{ID: 41, Name: name}, nil
	})

	const waiters = 8
	results := make(chan resolveResult, waiters)
	for range waiters {
		go func() {
			id, err := resolver.Resolve(context.Background(), "-1001", "Deploy")
			results <- resolveResult{id: id, err: err}
		}()
	}
	receive(t, started)
	close(release)
	for range waiters {
		result := receive(t, results)
		if result.err != nil || result.id != 41 {
			t.Fatalf("Resolve() = (%d, %v), want (41, nil)", result.id, result.err)
		}
	}

	id, err := resolver.Resolve(context.Background(), "-1001", "Deploy")
	if err != nil || id != 41 {
		t.Fatalf("cached Resolve() = (%d, %v), want (41, nil)", id, err)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("create calls = %d, want 1", got)
	}
}

func TestResolverUsesExactChatIDAndNameKey(t *testing.T) {
	var calls atomic.Int64
	resolver := NewResolver(context.Background(), 10, time.Second, func(_ context.Context, _, name string) (telegram.Topic, error) {
		return telegram.Topic{ID: calls.Add(1), Name: name}, nil
	})

	first, err := resolver.Resolve(context.Background(), "shared-chat", "ops")
	if err != nil {
		t.Fatal(err)
	}
	alias, err := resolver.Resolve(context.Background(), "shared-chat", "ops")
	if err != nil {
		t.Fatal(err)
	}
	differentCase, err := resolver.Resolve(context.Background(), "shared-chat", "Ops")
	if err != nil {
		t.Fatal(err)
	}
	differentChat, err := resolver.Resolve(context.Background(), "other-chat", "ops")
	if err != nil {
		t.Fatal(err)
	}

	if first != alias || differentCase == first || differentChat == first || calls.Load() != 3 {
		t.Fatalf("IDs = (%d, %d, %d, %d), calls = %d; want alias shared and exact distinct keys", first, alias, differentCase, differentChat, calls.Load())
	}
}

func TestResolverDoesNotHoldMutexDuringCreation(t *testing.T) {
	firstStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	resolver := NewResolver(context.Background(), 10, time.Second, func(_ context.Context, _, name string) (telegram.Topic, error) {
		if name == "slow" {
			close(firstStarted)
			<-releaseFirst
			return telegram.Topic{ID: 1, Name: name}, nil
		}
		return telegram.Topic{ID: 2, Name: name}, nil
	})

	slow := make(chan resolveResult, 1)
	go func() {
		id, err := resolver.Resolve(context.Background(), "chat", "slow")
		slow <- resolveResult{id: id, err: err}
	}()
	receive(t, firstStarted)

	fast := make(chan resolveResult, 1)
	go func() {
		id, err := resolver.Resolve(context.Background(), "chat", "fast")
		fast <- resolveResult{id: id, err: err}
	}()
	if result := receive(t, fast); result.err != nil || result.id != 2 {
		t.Fatalf("independent Resolve() = (%d, %v), want (2, nil)", result.id, result.err)
	}
	close(releaseFirst)
	if result := receive(t, slow); result.err != nil || result.id != 1 {
		t.Fatalf("slow Resolve() = (%d, %v), want (1, nil)", result.id, result.err)
	}
}

func TestResolverCapacityCountsPendingAndPreservesExistingKeys(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	resolver := NewResolver(context.Background(), 1, time.Second, func(_ context.Context, _, name string) (telegram.Topic, error) {
		close(started)
		<-release
		return telegram.Topic{ID: 7, Name: name}, nil
	})

	first := make(chan resolveResult, 1)
	go func() {
		id, err := resolver.Resolve(context.Background(), "chat", "one")
		first <- resolveResult{id: id, err: err}
	}()
	receive(t, started)
	if _, err := resolver.Resolve(context.Background(), "chat", "two"); !errors.Is(err, ErrCapacity) {
		t.Fatalf("new key error = %v, want ErrCapacity", err)
	}
	close(release)
	if result := receive(t, first); result.err != nil || result.id != 7 {
		t.Fatalf("first Resolve() = (%d, %v), want (7, nil)", result.id, result.err)
	}
	if id, err := resolver.Resolve(context.Background(), "chat", "one"); err != nil || id != 7 {
		t.Fatalf("existing key Resolve() = (%d, %v), want (7, nil)", id, err)
	}
	if _, err := resolver.Resolve(context.Background(), "chat", "two"); !errors.Is(err, ErrCapacity) {
		t.Fatalf("new key after success error = %v, want known entry to consume capacity", err)
	}
}

func TestResolverCallerCancellationDoesNotCancelSharedCreation(t *testing.T) {
	started := make(chan context.Context, 1)
	release := make(chan struct{})
	resolver := NewResolver(context.Background(), 10, time.Second, func(ctx context.Context, _, name string) (telegram.Topic, error) {
		started <- ctx
		<-release
		return telegram.Topic{ID: 52, Name: name}, nil
	})

	callerCtx, cancelCaller := context.WithCancel(context.Background())
	first := make(chan error, 1)
	go func() {
		_, err := resolver.Resolve(callerCtx, "chat", "name")
		first <- err
	}()
	createCtx := receive(t, started)
	cancelCaller()
	if err := receive(t, first); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled waiter error = %v, want context.Canceled", err)
	}
	if err := createCtx.Err(); err != nil {
		t.Fatalf("create context cancelled with first waiter: %v", err)
	}

	second := make(chan resolveResult, 1)
	go func() {
		id, err := resolver.Resolve(context.Background(), "chat", "name")
		second <- resolveResult{id: id, err: err}
	}()
	close(release)
	if result := receive(t, second); result.err != nil || result.id != 52 {
		t.Fatalf("second waiter = (%d, %v), want (52, nil)", result.id, result.err)
	}
}

func TestResolverShutdownCancelsCreation(t *testing.T) {
	shutdown, stop := context.WithCancel(context.Background())
	started := make(chan struct{})
	resolver := NewResolver(shutdown, 10, time.Second, func(ctx context.Context, _, _ string) (telegram.Topic, error) {
		close(started)
		<-ctx.Done()
		return telegram.Topic{}, ctx.Err()
	})

	result := make(chan error, 1)
	go func() {
		_, err := resolver.Resolve(context.Background(), "chat", "name")
		result <- err
	}()
	receive(t, started)
	stop()
	if err := receive(t, result); !errors.Is(err, context.Canceled) {
		t.Fatalf("Resolve error = %v, want shutdown cancellation", err)
	}
}

func TestResolverCreationTimeout(t *testing.T) {
	resolver := NewResolver(context.Background(), 10, 10*time.Millisecond, func(ctx context.Context, _, _ string) (telegram.Topic, error) {
		<-ctx.Done()
		return telegram.Topic{}, ctx.Err()
	})

	if _, err := resolver.Resolve(context.Background(), "chat", "name"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Resolve error = %v, want context.DeadlineExceeded", err)
	}
}

func TestResolverClearFailureReleasesKeyForRetry(t *testing.T) {
	clearFailure := &telegram.Error{Code: "telegram_transport", Message: "not sent", HTTPStatus: 502}
	var calls atomic.Int32
	resolver := NewResolver(context.Background(), 1, time.Second, func(_ context.Context, _, name string) (telegram.Topic, error) {
		if calls.Add(1) == 1 {
			return telegram.Topic{}, clearFailure
		}
		return telegram.Topic{ID: 88, Name: name}, nil
	})

	if _, err := resolver.Resolve(context.Background(), "chat", "name"); err != clearFailure {
		t.Fatalf("first error = %v, want original Telegram error %v", err, clearFailure)
	}
	if id, err := resolver.Resolve(context.Background(), "chat", "name"); err != nil || id != 88 {
		t.Fatalf("retry Resolve() = (%d, %v), want (88, nil)", id, err)
	}
	if calls.Load() != 2 {
		t.Fatalf("create calls = %d, want 2", calls.Load())
	}
}

func TestResolverRetainsUncertainFailure(t *testing.T) {
	telegramFailure := &telegram.Error{Code: "telegram_uncertain", Message: "maybe created", HTTPStatus: 502, Uncertain: true}
	uncertainFailure := fmt.Errorf("create topic: %w", telegramFailure)
	var calls atomic.Int32
	const waiters = 4
	ready := make(chan struct{})
	release := make(chan struct{})
	resolver := NewResolver(context.Background(), 1, time.Second, func(_ context.Context, _, _ string) (telegram.Topic, error) {
		calls.Add(1)
		close(ready)
		<-release
		return telegram.Topic{}, uncertainFailure
	})
	results := make(chan error, waiters)
	go func() {
		_, err := resolver.Resolve(context.Background(), "chat", "name")
		results <- err
	}()
	receive(t, ready)
	for range waiters - 1 {
		waiting := make(chan struct{})
		ctx := &waitingContext{Context: context.Background(), waiting: waiting}
		go func() {
			_, err := resolver.Resolve(ctx, "chat", "name")
			results <- err
		}()
		receive(t, waiting)
	}
	close(release)
	for range waiters {
		if err := receive(t, results); err != uncertainFailure {
			t.Fatalf("initial waiter error = %v, want original %v", err, uncertainFailure)
		}
	}
	if _, err := resolver.Resolve(context.Background(), "chat", "name"); !errors.Is(err, ErrUncertain) {
		t.Fatalf("later same-key error = %v, want ErrUncertain", err)
	}
	if _, err := resolver.Resolve(context.Background(), "chat", "other"); !errors.Is(err, ErrCapacity) {
		t.Fatalf("new-key error = %v, want retained entry to consume capacity", err)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("create calls = %d, want 1", got)
	}
}

func TestResolverInvalidSuccessfulIDIsRetainedAsUncertain(t *testing.T) {
	for _, invalidID := range []int64{-1, 0} {
		t.Run(fmt.Sprintf("id_%d", invalidID), func(t *testing.T) {
			var calls atomic.Int32
			resolver := NewResolver(context.Background(), 1, time.Second, func(_ context.Context, _, name string) (telegram.Topic, error) {
				calls.Add(1)
				return telegram.Topic{ID: invalidID, Name: name}, nil
			})

			if _, err := resolver.Resolve(context.Background(), "chat", "name"); !errors.Is(err, ErrUncertain) {
				t.Fatalf("initial invalid-ID error = %v, want ErrUncertain", err)
			}
			if _, err := resolver.Resolve(context.Background(), "chat", "name"); !errors.Is(err, ErrUncertain) {
				t.Fatalf("later invalid-ID error = %v, want ErrUncertain", err)
			}
			if calls.Load() != 1 {
				t.Fatalf("create calls = %d, want 1", calls.Load())
			}
		})
	}
}

func TestResolverNewInstanceForgetsMappings(t *testing.T) {
	var calls atomic.Int64
	create := func(_ context.Context, _, name string) (telegram.Topic, error) {
		return telegram.Topic{ID: calls.Add(1), Name: name}, nil
	}
	first := NewResolver(context.Background(), 10, time.Second, create)
	if id, err := first.Resolve(context.Background(), "chat", "name"); err != nil || id != 1 {
		t.Fatalf("first resolver = (%d, %v), want (1, nil)", id, err)
	}
	second := NewResolver(context.Background(), 10, time.Second, create)
	if id, err := second.Resolve(context.Background(), "chat", "name"); err != nil || id != 2 {
		t.Fatalf("new resolver = (%d, %v), want (2, nil)", id, err)
	}
}

func TestResolverKeepsKnownIDAfterExternalOperationErrors(t *testing.T) {
	var calls atomic.Int32
	resolver := NewResolver(context.Background(), 10, time.Second, func(_ context.Context, _, name string) (telegram.Topic, error) {
		calls.Add(1)
		return telegram.Topic{ID: 73, Name: name}, nil
	})

	id, err := resolver.Resolve(context.Background(), "chat", "name")
	if err != nil || id != 73 {
		t.Fatalf("initial Resolve() = (%d, %v), want (73, nil)", id, err)
	}
	// Send and delete happen outside the resolver. Their failures provide no
	// resolver mutation hook, so the next resolution must retain the known ID.
	id, err = resolver.Resolve(context.Background(), "chat", "name")
	if err != nil || id != 73 {
		t.Fatalf("Resolve() after external errors = (%d, %v), want (73, nil)", id, err)
	}
	if calls.Load() != 1 {
		t.Fatalf("create calls = %d, want 1", calls.Load())
	}
}

type resolveResult struct {
	id  int64
	err error
}

type waitingContext struct {
	context.Context
	waiting chan struct{}
}

func (c *waitingContext) Done() <-chan struct{} {
	select {
	case <-c.waiting:
	default:
		close(c.waiting)
	}
	return c.Context.Done()
}

func receive[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case value := <-ch:
		return value
	case <-time.After(resolverTestTimeout):
		t.Fatal("timed out waiting for test coordination")
		var zero T
		return zero
	}
}
