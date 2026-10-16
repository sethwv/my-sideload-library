package index

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
)

const debounceDelay = 2 * time.Second

// Watcher incrementally keeps the index in sync with filesystem changes
// under libraryRoots, debouncing bursts of events per file.
type Watcher struct {
	fsw          *fsnotify.Watcher
	libraryRoots []string
	db           *DB
	saver        CoverSaver

	mu     sync.Mutex
	timers map[string]*time.Timer
}

// NewWatcher creates a watcher and registers all existing subdirectories of
// each of libraryRoots. fsnotify does not watch recursively, so each
// directory must be added explicitly. Context cancellation interrupts a
// potentially long traversal of a large library.
func NewWatcher(ctx context.Context, libraryRoots []string, db *DB, saver CoverSaver) (*Watcher, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	fsw, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}

	w := &Watcher{
		fsw:          fsw,
		libraryRoots: libraryRoots,
		db:           db,
		saver:        saver,
		timers:       make(map[string]*time.Timer),
	}

	for _, root := range libraryRoots {
		err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			if err != nil {
				return nil
			}
			if d.IsDir() {
				if werr := fsw.Add(path); werr != nil {
					logger.Error("watch: failed to watch")
				}
			}
			return nil
		})
		if err != nil {
			fsw.Close()
			return nil, err
		}
	}

	return w, nil
}

// Run processes filesystem events until ctx is cancelled.
func (w *Watcher) Run(ctx context.Context) {
	defer w.fsw.Close()
	for {
		select {
		case <-ctx.Done():
			return
		case event, ok := <-w.fsw.Events:
			if !ok {
				return
			}
			w.handleEvent(event)
		case _, ok := <-w.fsw.Errors:
			if !ok {
				return
			}
			logger.Error("watch: error")
		}
	}
}

func (w *Watcher) handleEvent(event fsnotify.Event) {
	// New directories need to be watched too, so files added inside them are seen.
	if event.Has(fsnotify.Create) {
		if info, err := os.Stat(event.Name); err == nil && info.IsDir() {
			if err := w.fsw.Add(event.Name); err != nil {
				logger.Error("watch: failed to watch new dir")
			}
			return
		}
	}

	if !strings.EqualFold(filepath.Ext(event.Name), ".epub") {
		return
	}

	root, ok := w.rootFor(event.Name)
	if !ok {
		return
	}

	rel, err := filepath.Rel(root, event.Name)
	if err != nil {
		return
	}

	w.debounced(root+"\x00"+rel, func() {
		if event.Has(fsnotify.Remove) || event.Has(fsnotify.Rename) {
			if err := w.db.DeleteByPath(root, rel); err != nil {
				logger.Error("watch: delete")
			}
			return
		}
		if err := w.db.UpsertPath(root, rel, w.saver); err != nil {
			// File may have been removed between the event firing and this running
			// (e.g. a rename shows up as create+delete); treat as non-fatal.
			logger.Error("watch: upsert")
		}
	})
}

// rootFor returns which configured library root absPath falls under. If
// roots are nested, the longest (most specific) match wins.
func (w *Watcher) rootFor(absPath string) (string, bool) {
	best := ""
	for _, root := range w.libraryRoots {
		if absPath != root && !strings.HasPrefix(absPath, root+string(filepath.Separator)) {
			continue
		}
		if len(root) > len(best) {
			best = root
		}
	}
	if best == "" {
		return "", false
	}
	return best, true
}

func (w *Watcher) debounced(key string, fn func()) {
	w.mu.Lock()
	defer w.mu.Unlock()

	if t, ok := w.timers[key]; ok {
		t.Stop()
	}
	w.timers[key] = time.AfterFunc(debounceDelay, func() {
		fn()
		w.mu.Lock()
		delete(w.timers, key)
		w.mu.Unlock()
	})
}
