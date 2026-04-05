package watchers

import (
	"os/exec"
	"strings"
	"time"

	"github.com/charmbracelet/log"
)

type GitWatcher struct {
	dir      string
	interval time.Duration
	done     chan struct{}
	lastHead string
}

func NewGitWatcher(dir string) *GitWatcher {
	return &GitWatcher{
		dir:      dir,
		interval: 5 * time.Second,
		done:     make(chan struct{}),
	}
}

func (gw *GitWatcher) Name() string { return "git" }

func (gw *GitWatcher) Start(events chan<- Event) error {
	gw.lastHead = gw.currentHead()

	go func() {
		ticker := time.NewTicker(gw.interval)
		defer ticker.Stop()

		for {
			select {
			case <-gw.done:
				return
			case <-ticker.C:
				gw.poll(events)
			}
		}
	}()

	log.Info("git watcher started", "dir", gw.dir)
	return nil
}

func (gw *GitWatcher) Stop() error {
	close(gw.done)
	return nil
}

func (gw *GitWatcher) poll(events chan<- Event) {
	head := gw.currentHead()
	if head == "" || head == gw.lastHead {
		return
	}

	// New commit detected
	msg := gw.commitMessage(head)
	branch := gw.currentBranch()

	events <- Event{
		Source: "git",
		Type:   "commit",
		Payload: map[string]any{
			"sha":     head,
			"prev":    gw.lastHead,
			"message": msg,
			"branch":  branch,
		},
		Timestamp: time.Now(),
	}

	gw.lastHead = head
}

func (gw *GitWatcher) currentHead() string {
	return gw.git("rev-parse", "HEAD")
}

func (gw *GitWatcher) currentBranch() string {
	return gw.git("rev-parse", "--abbrev-ref", "HEAD")
}

func (gw *GitWatcher) commitMessage(sha string) string {
	return gw.git("log", "-1", "--format=%s", sha)
}

func (gw *GitWatcher) git(args ...string) string {
	cmd := exec.Command("git", args...)
	cmd.Dir = gw.dir
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
