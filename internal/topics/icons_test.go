package topics

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/creatorofuniverses/gong/internal/telegram"
)

var fallbackIconColors = []int{7322096, 16766590, 13338331, 9367192, 16749490, 16478047}

func expectedCatalogueIcon(name string, ids []string) telegram.Icon {
	digest := sha256.Sum256([]byte(name))
	index := binary.BigEndian.Uint64(digest[:8]) % uint64(len(ids))
	return telegram.Icon{CustomEmojiID: ids[index]}
}

func expectedFallbackIcon(name string) telegram.Icon {
	digest := sha256.Sum256([]byte(name))
	index := binary.BigEndian.Uint64(digest[:8]) % uint64(len(fallbackIconColors))
	return telegram.Icon{Color: fallbackIconColors[index]}
}

func testLogger(buf *bytes.Buffer) *slog.Logger {
	return slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelWarn}))
}

func TestIconPickerDeduplicatesSortsAndHashesNames(t *testing.T) {
	var loads atomic.Int32
	picker := NewIconPicker(context.Background(), func(context.Context) ([]string, error) {
		loads.Add(1)
		return []string{"emoji-b", "", "emoji-a", "emoji-b", "emoji-c"}, nil
	}, slog.New(slog.NewTextHandler(bytes.NewBuffer(nil), nil)))

	for _, name := range []string{"same name", "same name", "тема 🔔"} {
		got, err := picker.Pick(context.Background(), name)
		if err != nil {
			t.Fatalf("Pick(%q): %v", name, err)
		}
		ids := []string{"emoji-a", "emoji-b", "emoji-c"}
		want := expectedCatalogueIcon(name, ids)
		if got != want {
			t.Errorf("Pick(%q) = %+v, want %+v", name, got, want)
		}
	}
	if got := loads.Load(); got != 1 {
		t.Fatalf("catalogue loaded %d times, want once", got)
	}
}

func TestIconPickerUsesLiteralHashFixtures(t *testing.T) {
	picker := NewIconPicker(context.Background(), func(context.Context) ([]string, error) {
		return []string{"30", "10", "20", "10"}, nil
	}, nil)

	icon, err := picker.Pick(context.Background(), "backup")
	if err != nil {
		t.Fatalf("Pick: %v", err)
	}
	if want := (telegram.Icon{CustomEmojiID: "30"}); icon != want {
		t.Fatalf("Pick = %+v, want literal fixture %+v", icon, want)
	}
}

func TestIconPickerUsesLiteralFallbackFixture(t *testing.T) {
	picker := NewIconPicker(context.Background(), func(context.Context) ([]string, error) {
		return []string{}, nil
	}, nil)

	icon, err := picker.Pick(context.Background(), "backup")
	if err != nil {
		t.Fatalf("Pick: %v", err)
	}
	if want := (telegram.Icon{Color: 13338331}); icon != want {
		t.Fatalf("Pick = %+v, want literal fixture %+v", icon, want)
	}
}

func TestIconPickerIsStableAcrossCatalogueOrderAndRestart(t *testing.T) {
	load := func(ids []string) func(context.Context) ([]string, error) {
		return func(context.Context) ([]string, error) { return ids, nil }
	}
	first := NewIconPicker(context.Background(), load([]string{"b", "a", "c", "b"}), nil)
	second := NewIconPicker(context.Background(), load([]string{"c", "b", "a"}), nil)
	for _, name := range []string{"backup", "ночь", "same"} {
		one, err := first.Pick(context.Background(), name)
		if err != nil {
			t.Fatalf("first Pick(%q): %v", name, err)
		}
		two, err := second.Pick(context.Background(), name)
		if err != nil {
			t.Fatalf("second Pick(%q): %v", name, err)
		}
		if one != two {
			t.Errorf("Pick(%q) changed after restart/order change: %+v vs %+v", name, one, two)
		}
	}
}

func TestIconPickerHonorsCancelledWaiterWithoutCancellingSharedLoad(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	var loads atomic.Int32
	picker := NewIconPicker(context.Background(), func(context.Context) ([]string, error) {
		loads.Add(1)
		close(started)
		<-release
		return []string{"emoji"}, nil
	}, nil)

	firstCtx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	firstDone := make(chan error, 1)
	go func() {
		_, err := picker.Pick(firstCtx, "first")
		firstDone <- err
	}()
	<-started
	if err := <-firstDone; !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("cancelled waiter error = %v, want deadline exceeded", err)
	}

	secondDone := make(chan telegram.Icon, 1)
	secondErr := make(chan error, 1)
	go func() {
		icon, err := picker.Pick(context.Background(), "second")
		secondDone <- icon
		secondErr <- err
	}()
	select {
	case <-secondErr:
		t.Fatal("second waiter returned before shared load was released")
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	if err := <-secondErr; err != nil {
		t.Fatalf("second waiter: %v", err)
	}
	if got := <-secondDone; got.CustomEmojiID != "emoji" {
		t.Fatalf("second waiter got %+v, want emoji", got)
	}
	if got := loads.Load(); got != 1 {
		t.Fatalf("catalogue loaded %d times, want once", got)
	}
}

func TestIconPickerLoadsOnceForParallelPickers(t *testing.T) {
	var loads atomic.Int32
	picker := NewIconPicker(context.Background(), func(context.Context) ([]string, error) {
		loads.Add(1)
		time.Sleep(10 * time.Millisecond)
		return []string{"emoji"}, nil
	}, nil)

	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := picker.Pick(context.Background(), "parallel"); err != nil {
				t.Errorf("parallel Pick: %v", err)
			}
		}()
	}
	wg.Wait()
	if got := loads.Load(); got != 1 {
		t.Fatalf("catalogue loaded %d times, want once", got)
	}
}

func TestIconPickerTimeoutUsesCachedFallbackAndWarnsOnce(t *testing.T) {
	var logs bytes.Buffer
	var loads atomic.Int32
	picker := NewIconPicker(context.Background(), func(ctx context.Context) ([]string, error) {
		loads.Add(1)
		<-ctx.Done()
		return nil, ctx.Err()
	}, testLogger(&logs))

	for _, name := range []string{"timeout", "again"} {
		icon, err := picker.Pick(context.Background(), name)
		if err != nil {
			t.Fatalf("Pick(%q): %v", name, err)
		}
		if want := expectedFallbackIcon(name); icon != want {
			t.Errorf("Pick(%q) = %+v, want %+v", name, icon, want)
		}
	}
	if got := loads.Load(); got != 1 {
		t.Fatalf("catalogue loaded %d times, want once", got)
	}
	if got := bytes.Count(logs.Bytes(), []byte("topic icon catalogue unavailable; using fallback")); got != 1 {
		t.Fatalf("fallback warning count = %d, want one", got)
	}
}

func TestIconPickerCachesEmptyAndFailedCataloguesWithoutLoaderError(t *testing.T) {
	for _, test := range []struct {
		name string
		load func() ([]string, error)
	}{
		{name: "empty", load: func() ([]string, error) { return []string{"", ""}, nil }},
		{name: "failed", load: func() ([]string, error) { return nil, errors.New("secret loader detail") }},
	} {
		t.Run(test.name, func(t *testing.T) {
			var logs bytes.Buffer
			var loads atomic.Int32
			picker := NewIconPicker(context.Background(), func(context.Context) ([]string, error) {
				loads.Add(1)
				return test.load()
			}, testLogger(&logs))
			for i := 0; i < 3; i++ {
				icon, err := picker.Pick(context.Background(), "fallback")
				if err != nil {
					t.Fatalf("Pick: %v", err)
				}
				if icon != expectedFallbackIcon("fallback") {
					t.Fatalf("Pick = %+v, want fallback", icon)
				}
			}
			if loads.Load() != 1 {
				t.Fatalf("catalogue loaded %d times, want once", loads.Load())
			}
			if bytes.Contains(logs.Bytes(), []byte("secret loader detail")) {
				t.Fatal("loader error was logged")
			}
			if got := bytes.Count(logs.Bytes(), []byte("topic icon catalogue unavailable; using fallback")); got != 1 {
				t.Fatalf("fallback warning count = %d, want one", got)
			}
		})
	}
}

func TestIconPickerHonorsCallerTimeoutWhileCatalogueContinues(t *testing.T) {
	started := make(chan struct{})
	picker := NewIconPicker(context.Background(), func(context.Context) ([]string, error) {
		close(started)
		time.Sleep(50 * time.Millisecond)
		return []string{"emoji"}, nil
	}, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	result := make(chan error, 1)
	go func() {
		_, err := picker.Pick(ctx, "timed")
		result <- err
	}()
	<-started
	if err := <-result; !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Pick error = %v, want deadline exceeded", err)
	}
	icon, err := picker.Pick(context.Background(), "timed")
	if err != nil {
		t.Fatalf("second Pick: %v", err)
	}
	if icon.CustomEmojiID != "emoji" {
		t.Fatalf("second Pick = %+v, want emoji", icon)
	}
}
