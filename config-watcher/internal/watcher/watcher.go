package watcher

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"time"

	"config-watcher/internal/actions"
	"config-watcher/internal/config"
)

type Manager struct {
	cfg    config.Config
	logger *slog.Logger
}

func NewManager(cfg config.Config, logger *slog.Logger) *Manager {
	return &Manager{
		cfg:    cfg,
		logger: logger,
	}
}

func (m *Manager) Start(ctx context.Context) error {
	for i := range m.cfg.Watch {
		w := m.cfg.Watch[i]
		go m.runWatch(ctx, w)
	}
	return nil
}

type fileState struct {
	exists bool
	size   int64
	mtime  time.Time
	hash   string
}

func (m *Manager) runWatch(ctx context.Context, w config.WatchConfig) {
	log := m.logger.With("path", w.Path, "action", w.Action.Type)
	act, err := actions.Build(w.Action)
	if err != nil {
		log.Error("failed to build action", "error", err)
		return
	}

	var current fileState
	for {
		current, err = readState(w.Path)
		if err == nil {
			break
		}
		if !errors.Is(err, os.ErrNotExist) || !w.WaitForFile {
			log.Warn("initial read failed", "error", err)
			if !w.WaitForFile {
				break
			}
		}

		select {
		case <-ctx.Done():
			return
		case <-time.After(2 * time.Second):
		}
	}

	log.Info("watcher initialized")

	ticker := time.NewTicker(w.Interval.Duration)
	defer ticker.Stop()

	var lastAction time.Time

	for {
		select {
		case <-ctx.Done():
			log.Info("watcher stopped")
			return
		case <-ticker.C:
			next, err := readState(w.Path)
			if err != nil {
				if errors.Is(err, os.ErrNotExist) {
					log.Warn("watched file missing")
					continue
				}
				log.Error("poll read failed", "error", err)
				continue
			}
			if !changed(current, next) {
				continue
			}

			if next.hash == current.hash {
				current = next
				continue
			}

			log.Info("detected potential file change")

			if w.Debounce.Duration > 0 {
				select {
				case <-ctx.Done():
					return
				case <-time.After(w.Debounce.Duration):
				}

				stable, err := readState(w.Path)
				if err != nil {
					log.Warn("debounce re-check failed", "error", err)
					continue
				}
				if stable.hash != next.hash {
					log.Info("change not stable after debounce")
					current = stable
					continue
				}
				next = stable
			}

			if w.Cooldown.Duration > 0 && !lastAction.IsZero() && time.Since(lastAction) < w.Cooldown.Duration {
				log.Info("action suppressed by cooldown")
				current = next
				continue
			}

			jitter := actions.JitterDuration(w.Jitter.Min.Duration, w.Jitter.Max.Duration)
			if jitter > 0 {
				select {
				case <-ctx.Done():
					return
				case <-time.After(jitter):
				}
			}

			actionCtx, cancel := context.WithTimeout(ctx, w.Action.Timeout.Duration+w.Debounce.Duration+5*time.Second)
			err = act.Execute(actionCtx)
			cancel()

			if err != nil {
				log.Error("action failed", "error", err)
				current = next
				continue
			}

			lastAction = time.Now()
			current = next
			log.Info("action executed successfully")
		}
	}
}

func readState(path string) (fileState, error) {
	info, err := os.Stat(path)
	if err != nil {
		return fileState{}, err
	}
	if info.IsDir() {
		return fileState{}, fmt.Errorf("%q is a directory, expected file", path)
	}

	h, err := sha256File(path)
	if err != nil {
		return fileState{}, err
	}
	return fileState{
		exists: true,
		size:   info.Size(),
		mtime:  info.ModTime(),
		hash:   h,
	}, nil
}

func changed(old fileState, newState fileState) bool {
	return old.exists != newState.exists ||
		old.size != newState.size ||
		!old.mtime.Equal(newState.mtime)
}

func sha256File(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	sum := sha256.New()
	if _, err := io.Copy(sum, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(sum.Sum(nil)), nil
}
