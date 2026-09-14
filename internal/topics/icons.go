package topics

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/creatorofuniverses/gong/internal/telegram"
)

const iconCatalogueTimeout = 2 * time.Second

var standardIconColors = [...]int{7322096, 16766590, 13338331, 9367192, 16749490, 16478047}

// IconPicker chooses a Telegram topic icon deterministically by topic name.
// Its catalogue is loaded at most once for the lifetime of the picker.
type IconPicker struct {
	shutdown context.Context
	load     func(context.Context) ([]string, error)
	logger   *slog.Logger

	loadOnce sync.Once
	loadDone chan struct{}
	icons    []string
}

type iconLoadResult struct {
	icons []string
	err   error
}

// NewIconPicker constructs a lazy picker. The loader is called only when the
// first name is picked, and its context is independent from that Pick caller.
func NewIconPicker(shutdown context.Context, load func(context.Context) ([]string, error), logger *slog.Logger) *IconPicker {
	if shutdown == nil {
		shutdown = context.Background()
	}
	return &IconPicker{
		shutdown: shutdown,
		load:     load,
		logger:   logger,
		loadDone: make(chan struct{}),
	}
}

// Pick returns the icon for name. A cancelled caller receives its context
// error, while an in-flight catalogue load continues for other callers.
func (p *IconPicker) Pick(ctx context.Context, name string) (telegram.Icon, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return telegram.Icon{}, err
	}
	p.loadOnce.Do(func() { go p.loadCatalogue() })

	select {
	case <-p.loadDone:
		if err := ctx.Err(); err != nil {
			return telegram.Icon{}, err
		}
	case <-ctx.Done():
		return telegram.Icon{}, ctx.Err()
	}

	digest := sha256.Sum256([]byte(name))
	if len(p.icons) > 0 {
		index := binary.BigEndian.Uint64(digest[:8]) % uint64(len(p.icons))
		return telegram.Icon{CustomEmojiID: p.icons[index]}, nil
	}
	index := binary.BigEndian.Uint64(digest[:8]) % uint64(len(standardIconColors))
	return telegram.Icon{Color: standardIconColors[index]}, nil
}

func (p *IconPicker) loadCatalogue() {
	defer close(p.loadDone)

	ctx, cancel := context.WithTimeout(p.shutdown, iconCatalogueTimeout)
	defer cancel()
	result := make(chan iconLoadResult, 1)
	go func() {
		if p.load == nil {
			result <- iconLoadResult{err: errors.New("icon catalogue loader is nil")}
			return
		}
		icons, err := p.load(ctx)
		result <- iconLoadResult{icons: icons, err: err}
	}()
	var loaded iconLoadResult
	select {
	case loaded = <-result:
		if err := ctx.Err(); err != nil {
			p.warnFallback()
			return
		}
	case <-ctx.Done():
		p.warnFallback()
		return
	}
	if loaded.err != nil {
		p.warnFallback()
		return
	}

	seen := make(map[string]struct{}, len(loaded.icons))
	for _, icon := range loaded.icons {
		if strings.TrimSpace(icon) == "" {
			continue
		}
		if _, ok := seen[icon]; ok {
			continue
		}
		seen[icon] = struct{}{}
		p.icons = append(p.icons, icon)
	}
	sort.Strings(p.icons)
	if len(p.icons) == 0 {
		p.warnFallback()
	}
}

func (p *IconPicker) warnFallback() {
	if p.logger != nil {
		p.logger.Warn("topic icon catalogue unavailable; using fallback")
	}
}
