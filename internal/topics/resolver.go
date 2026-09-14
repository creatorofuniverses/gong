package topics

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/creatorofuniverses/gong/internal/telegram"
)

var (
	ErrCapacity  = errors.New("topic resolver capacity exhausted")
	ErrUncertain = errors.New("topic creation outcome is uncertain")
)

type resolverState uint8

const (
	resolverPending resolverState = iota
	resolverKnown
	resolverUncertain
)

type resolverKey struct {
	chatID string
	name   string
}

type resolverEntry struct {
	state resolverState
	done  chan struct{}
	id    int64
	err   error
}

// Resolver keeps the process-local mapping from an exact chat ID and topic
// name to the topic created for that key.
type Resolver struct {
	shutdown context.Context
	max      int
	timeout  time.Duration
	create   func(context.Context, string, string) (telegram.Topic, error)

	mu      sync.Mutex
	entries map[resolverKey]*resolverEntry
}

func NewResolver(
	shutdown context.Context,
	max int,
	timeout time.Duration,
	create func(context.Context, string, string) (telegram.Topic, error),
) *Resolver {
	if shutdown == nil {
		shutdown = context.Background()
	}
	return &Resolver{
		shutdown: shutdown,
		max:      max,
		timeout:  timeout,
		create:   create,
		entries:  make(map[resolverKey]*resolverEntry),
	}
}

// Resolve returns the remembered topic ID or shares one in-flight creation for
// the exact chatID and name. The creation lifetime is independent of ctx.
func (r *Resolver) Resolve(ctx context.Context, chatID, name string) (int64, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}

	key := resolverKey{chatID: chatID, name: name}
	r.mu.Lock()
	if entry, ok := r.entries[key]; ok {
		switch entry.state {
		case resolverKnown:
			id := entry.id
			r.mu.Unlock()
			return id, nil
		case resolverUncertain:
			r.mu.Unlock()
			return 0, ErrUncertain
		default:
			r.mu.Unlock()
			return waitForResolution(ctx, entry)
		}
	}
	if len(r.entries) >= r.max {
		r.mu.Unlock()
		return 0, ErrCapacity
	}

	entry := &resolverEntry{state: resolverPending, done: make(chan struct{})}
	r.entries[key] = entry
	r.mu.Unlock()

	go r.createTopic(key, entry)
	return waitForResolution(ctx, entry)
}

func (r *Resolver) createTopic(key resolverKey, entry *resolverEntry) {
	ctx, cancel := context.WithTimeout(r.shutdown, r.timeout)
	topic, err := r.create(ctx, key.chatID, key.name)
	cancel()

	r.mu.Lock()
	defer r.mu.Unlock()

	if err != nil {
		entry.err = err
		var telegramError *telegram.Error
		if errors.As(err, &telegramError) && telegramError.Uncertain {
			entry.state = resolverUncertain
		} else {
			delete(r.entries, key)
		}
		close(entry.done)
		return
	}
	if topic.ID <= 0 {
		entry.state = resolverUncertain
		entry.err = ErrUncertain
		close(entry.done)
		return
	}

	entry.state = resolverKnown
	entry.id = topic.ID
	close(entry.done)
}

func waitForResolution(ctx context.Context, entry *resolverEntry) (int64, error) {
	select {
	case <-entry.done:
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		return entry.id, entry.err
	case <-ctx.Done():
		return 0, ctx.Err()
	}
}
