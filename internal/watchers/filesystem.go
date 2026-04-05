package watchers

import (
	"path/filepath"
	"time"

	"github.com/charmbracelet/log"
	"github.com/fsnotify/fsnotify"
)

type FilesystemWatcher struct {
	watcher *fsnotify.Watcher
	dir     string
	done    chan struct{}
}

func NewFilesystemWatcher(dir string) (*FilesystemWatcher, error) {
	w, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}
	return &FilesystemWatcher{
		watcher: w,
		dir:     dir,
		done:    make(chan struct{}),
	}, nil
}

func (fw *FilesystemWatcher) Name() string { return "filesystem" }

func (fw *FilesystemWatcher) Start(events chan<- Event) error {
	if err := fw.watcher.Add(fw.dir); err != nil {
		return err
	}

	go func() {
		// Debounce: batch rapid file changes
		var pending map[string]fsnotify.Event
		timer := time.NewTimer(0)
		if !timer.Stop() {
			<-timer.C
		}
		pending = make(map[string]fsnotify.Event)

		for {
			select {
			case <-fw.done:
				return
			case event, ok := <-fw.watcher.Events:
				if !ok {
					return
				}
				if shouldIgnore(event.Name) {
					continue
				}
				pending[event.Name] = event
				timer.Reset(200 * time.Millisecond)
			case err, ok := <-fw.watcher.Errors:
				if !ok {
					return
				}
				log.Error("filesystem watcher error", "error", err)
			case <-timer.C:
				for _, event := range pending {
					events <- Event{
						Source: "filesystem",
						Type:   opString(event.Op),
						Payload: map[string]any{
							"file": event.Name,
							"op":   opString(event.Op),
						},
						Timestamp: time.Now(),
					}
				}
				pending = make(map[string]fsnotify.Event)
			}
		}
	}()

	log.Info("filesystem watcher started", "dir", fw.dir)
	return nil
}

func (fw *FilesystemWatcher) Stop() error {
	close(fw.done)
	return fw.watcher.Close()
}

func opString(op fsnotify.Op) string {
	switch {
	case op.Has(fsnotify.Create):
		return "file.created"
	case op.Has(fsnotify.Write):
		return "file.changed"
	case op.Has(fsnotify.Remove):
		return "file.removed"
	case op.Has(fsnotify.Rename):
		return "file.renamed"
	default:
		return "file.changed"
	}
}

func shouldIgnore(path string) bool {
	base := filepath.Base(path)
	ext := filepath.Ext(path)

	// Ignore hidden files, temp files, build artifacts
	ignorePatterns := []string{".git", ".DS_Store", "__pycache__", "node_modules", ".swp", ".swo"}
	for _, p := range ignorePatterns {
		if base == p || ext == p {
			return true
		}
	}

	// Ignore files starting with .
	if len(base) > 0 && base[0] == '.' {
		return true
	}

	return false
}
