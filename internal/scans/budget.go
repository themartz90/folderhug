package scans

import (
	"context"
	"time"
)

const phaseLimit = 30 * time.Second

// Reserve a bounded phase before doing work. A crash retains the reservation;
// normal completion replaces it with actual time. No offline time is charged.
func (s *Service) reservePhase(ctx context.Context, j job, consumed time.Duration) (context.Context, context.CancelFunc, error) {
	remaining := time.Duration((float64(*j.Input.MaxSeconds)-j.ElapsedSeconds)*float64(time.Second)) - consumed
	if remaining <= 0 {
		return nil, nil, context.DeadlineExceeded
	}
	budget := min(remaining, phaseLimit)
	reserved := j.ElapsedSeconds + consumed.Seconds() + budget.Seconds()
	if remaining <= phaseLimit {
		reserved = float64(*j.Input.MaxSeconds)
	}
	phase, cancel := context.WithTimeout(ctx, budget)
	result, err := s.db.ExecContext(phase, "UPDATE scan_jobs SET elapsed_seconds=?,active_since=? WHERE id=? AND state='running'", reserved, stamp(time.Now()), j.ID)
	if err == nil {
		var n int64
		n, err = result.RowsAffected()
		if err == nil && n != 1 {
			err = ErrInvalidState
		}
	}
	if err != nil {
		cancel()
		return nil, nil, err
	}
	return phase, cancel, nil
}

func (s *Service) settlePhase(ctx context.Context, j job, started time.Time) error {
	_, err := s.db.ExecContext(ctx, "UPDATE scan_jobs SET elapsed_seconds=?,active_since=NULL WHERE id=?", j.ElapsedSeconds+time.Since(started).Seconds(), j.ID)
	return err
}
