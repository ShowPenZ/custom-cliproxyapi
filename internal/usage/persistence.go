package usage

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"
)

const persistenceDebounce = 2 * time.Second

type persistedStatisticsSnapshot struct {
	Version int                `json:"version"`
	SavedAt time.Time          `json:"saved_at"`
	Usage   StatisticsSnapshot `json:"usage"`
}

type statsPersistence struct {
	mu      sync.RWMutex
	path    string
	trigger chan struct{}
}

// DefaultPersistencePath returns the default on-disk snapshot path used for usage statistics.
func DefaultPersistencePath(authDir string) string {
	authDir = strings.TrimSpace(authDir)
	if authDir == "" {
		return ""
	}
	return filepath.Join(authDir, "usage-stats.json")
}

// EnablePersistence loads any existing snapshot and enables async persistence for future updates.
func (s *RequestStatistics) EnablePersistence(path string) error {
	if s == nil {
		return nil
	}
	path = strings.TrimSpace(path)
	if path == "" {
		return nil
	}
	if !filepath.IsAbs(path) {
		abs, err := filepath.Abs(path)
		if err != nil {
			return err
		}
		path = abs
	}
	if err := s.loadPersistedSnapshot(path); err != nil {
		return err
	}

	s.mu.Lock()
	if s.persistence == nil {
		s.persistence = &statsPersistence{
			path:    path,
			trigger: make(chan struct{}, 1),
		}
		go s.persistence.loop(s)
	} else {
		s.persistence.setPath(path)
	}
	s.mu.Unlock()
	return nil
}

// PersistNow writes the current statistics snapshot to disk immediately.
func (s *RequestStatistics) PersistNow() error {
	if s == nil {
		return nil
	}
	path := s.persistencePath()
	if path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}

	payload := persistedStatisticsSnapshot{
		Version: 1,
		SavedAt: time.Now().UTC(),
		Usage:   s.Snapshot(),
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	tmpPath := path + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}

func (s *RequestStatistics) requestPersist() {
	if s == nil {
		return
	}
	s.mu.RLock()
	persistence := s.persistence
	s.mu.RUnlock()
	if persistence == nil {
		return
	}
	select {
	case persistence.trigger <- struct{}{}:
	default:
	}
}

func (s *RequestStatistics) persistencePath() string {
	s.mu.RLock()
	persistence := s.persistence
	s.mu.RUnlock()
	if persistence == nil {
		return ""
	}
	return persistence.getPath()
}

func (s *RequestStatistics) loadPersistedSnapshot(path string) error {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	data = []byte(strings.TrimSpace(string(data)))
	if len(data) == 0 {
		return nil
	}

	var persisted persistedStatisticsSnapshot
	if err := json.Unmarshal(data, &persisted); err == nil && persisted.Version <= 1 {
		s.MergeSnapshot(persisted.Usage)
		return nil
	}

	var snapshot StatisticsSnapshot
	if err := json.Unmarshal(data, &snapshot); err != nil {
		return err
	}
	s.MergeSnapshot(snapshot)
	return nil
}

func (p *statsPersistence) loop(stats *RequestStatistics) {
	for range p.trigger {
		timer := time.NewTimer(persistenceDebounce)
		for {
			select {
			case <-p.trigger:
				if !timer.Stop() {
					select {
					case <-timer.C:
					default:
					}
				}
				timer.Reset(persistenceDebounce)
			case <-timer.C:
				if err := stats.PersistNow(); err != nil {
					log.Warnf("usage persistence save failed: %v", err)
				}
				goto next
			}
		}
	next:
	}
}

func (p *statsPersistence) getPath() string {
	if p == nil {
		return ""
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.path
}

func (p *statsPersistence) setPath(path string) {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.path = path
}
