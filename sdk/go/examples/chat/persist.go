package main

import (
	"encoding/binary"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"

	"github.com/cockroachdb/pebble"
)

// messageStore persists chat messages in a PebbleDB key-value store.
// Keys are 8-byte big-endian sequence numbers increasing monotonically.
type messageStore struct {
	db   *pebble.DB
	mu   sync.Mutex
	next uint64
}

func openMessageStore(dir string) (*messageStore, error) {
	if dir == "" {
		return nil, nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	// Pebble database lives directly at the provided directory.
	// Accept default options for simplicity.
	db, err := pebble.Open(filepath.Clean(dir), &pebble.Options{})
	if err != nil {
		return nil, err
	}
	s := &messageStore{db: db}
	// Discover next sequence by reading the last key.
	it, err := db.NewIter(nil)
	if err != nil {
		return nil, err
	}
	defer it.Close()
	if it.Last() {
		if len(it.Key()) >= 8 {
			s.next = binary.BigEndian.Uint64(it.Key()[:8]) + 1
		}
	}
	return s, nil
}

func (s *messageStore) Append(m message) error {
	if s == nil || s.db == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := make([]byte, 8)
	binary.BigEndian.PutUint64(key, s.next)
	s.next++
	val, _ := json.Marshal(m)
	return s.db.Set(key, val, pebble.Sync)
}

func (s *messageStore) LoadAll() ([]message, error) {
	if s == nil || s.db == nil {
		return nil, nil
	}
	it, err := s.db.NewIter(nil)
	if err != nil {
		return nil, err
	}
	defer it.Close()
	out := make([]message, 0, 256)
	for it.First(); it.Valid(); it.Next() {
		var m message
		if err := json.Unmarshal(it.Value(), &m); err == nil {
			out = append(out, m)
		}
	}
	return out, nil
}

func (s *messageStore) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}
