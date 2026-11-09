package receipts

import (
	"fmt"
	"sort"
	"sync"
	"time"
)

// Mock is an in-memory Store for tests. PutErr, when set, is returned by every
// Put (a store that is full or unwritable).
type Mock struct {
	mu     sync.Mutex
	byID   map[string]Receipt
	PutErr error
}

var _ Store = (*Mock)(nil)

// NewMock returns an empty Mock.
func NewMock() *Mock { return &Mock{byID: map[string]Receipt{}} }

// Put validates and keeps r, as FileStore does.
func (m *Mock) Put(r Receipt) error {
	if m.PutErr != nil {
		return m.PutErr
	}
	if _, err := Encode(r); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.byID[r.ID] = r
	return nil
}

// Get returns the receipt or an error wrapping ErrNotFound.
func (m *Mock) Get(id string) (Receipt, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.byID[id]
	if !ok {
		return Receipt{}, fmt.Errorf("%w: %q", ErrNotFound, id)
	}
	return r, nil
}

// All returns every kept receipt, oldest first.
func (m *Mock) All() []Receipt {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Receipt, 0, len(m.byID))
	for _, r := range m.byID {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].At.Equal(out[j].At) {
			return out[i].At.Before(out[j].At)
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// Prune applies the age and count limits of the clamped policy (the size limit
// is the file store's; a Mock holds no files).
func (m *Mock) Prune(p Policy) error {
	p = p.Clamp()
	all := m.All()
	cutoff := time.Now().Add(-p.MaxAge)
	m.mu.Lock()
	defer m.mu.Unlock()
	for i, r := range all {
		if r.At.After(cutoff) && len(all)-i <= p.MaxCount {
			break
		}
		delete(m.byID, r.ID)
	}
	return nil
}

// Usage counts the kept receipts; a Mock holds no files, so the size is the
// encoded size of each.
func (m *Mock) Usage() (int, int64, time.Time) {
	var total int64
	var oldest time.Time
	all := m.All()
	for _, r := range all {
		if b, err := Encode(r); err == nil {
			total += int64(len(b))
		}
	}
	if len(all) > 0 {
		oldest = all[0].At
	}
	return len(all), total, oldest
}

// Clear removes every receipt.
func (m *Mock) Clear() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.byID = map[string]Receipt{}
	return nil
}
