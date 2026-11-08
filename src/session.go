package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"syscall"
)

var sessionName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// Session is a JSON-file conversation history held under an exclusive lock.
type Session struct {
	Messages []Message `json:"messages"`
	path     string
	lock     *os.File
}

// OpenSession locks and loads the named session, creating it if absent.
func OpenSession(dir, name string) (*Session, error) {
	if !sessionName.MatchString(name) {
		return nil, fmt.Errorf("invalid session name %q (allowed: letters, digits, '.', '_', '-')", name)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, name+".json")
	lock, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		lock.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, fmt.Errorf("session %q is in use by another run", name)
		}
		return nil, err
	}
	s := &Session{path: path, lock: lock}
	data, err := os.ReadFile(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
	case err != nil:
		s.Close()
		return nil, err
	default:
		if err := json.Unmarshal(data, s); err != nil {
			s.Close()
			return nil, fmt.Errorf("read session %s: %w", path, err)
		}
	}
	return s, nil
}

// Save writes the session atomically (temp file + rename).
func (s *Session) Save() error {
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(s.path), ".session-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), s.path)
}

func (s *Session) Close() { s.lock.Close() }
