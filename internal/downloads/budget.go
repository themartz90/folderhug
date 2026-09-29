package downloads

import (
	"context"
	"os"
	"sync"
	"time"
)

type reservation struct {
	limit, physical, written int64
	checked                  time.Time
}

func (s *Service) reserve(id string, limit, physical, written int64) error {
	s.budgetMu.Lock()
	defer s.budgetMu.Unlock()
	s.reservations[id] = &reservation{limit: limit, physical: physical, written: written}
	if err := s.checkBudget(); err != nil {
		delete(s.reservations, id)
		return err
	}
	return nil
}
func (s *Service) release(id string) {
	s.budgetMu.Lock()
	delete(s.reservations, id)
	s.budgetMu.Unlock()
}

// Caller holds budgetMu. Logical file sizes count once against the queue quota;
// free-space reservations also include unwritten portions of sparse segments.
func (s *Service) checkBudget() error {
	used, err := s.files.UsedBytes()
	if err != nil {
		return err
	}
	var logical, unwritten int64
	for _, r := range s.reservations {
		logical += max(0, r.limit-r.physical)
		unwritten += max(0, r.limit-r.written)
	}
	if used > s.limits.StorageBudgetBytes || logical > s.limits.StorageBudgetBytes-used {
		return ErrBudget
	}
	available, err := s.files.AvailableBytes()
	if err != nil {
		return err
	}
	if available != nil && (*available < s.limits.FreeReserveBytes || unwritten > *available-s.limits.FreeReserveBytes) {
		return ErrBudget
	}
	return nil
}

func (s *Service) writePart(id string, f *os.File, data []byte, offset int64) error {
	s.budgetMu.Lock()
	defer s.budgetMu.Unlock()
	r := s.reservations[id]
	if r == nil || offset < 0 || int64(len(data)) > r.limit-offset {
		return ErrBudget
	}
	if time.Since(r.checked) >= time.Second {
		if err := s.checkBudget(); err != nil {
			return err
		}
		r.checked = time.Now()
	}
	n, err := f.WriteAt(data, offset)
	r.physical = max(r.physical, offset+int64(n))
	r.written += int64(n)
	if err != nil {
		return err
	}
	if n != len(data) {
		return os.ErrInvalid
	}
	return nil
}

type throttle struct {
	mu   sync.Mutex
	next time.Time
	bps  int64
}

func (t *throttle) wait(ctx context.Context, n int) error {
	if t.bps == 0 {
		return nil
	}
	t.mu.Lock()
	now := time.Now()
	if t.next.Before(now) {
		t.next = now
	}
	duration := time.Duration(float64(n) / float64(t.bps) * float64(time.Second))
	t.next = t.next.Add(duration)
	until := time.Until(t.next)
	t.mu.Unlock()
	timer := time.NewTimer(until)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
