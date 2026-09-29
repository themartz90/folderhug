// Package testresource supports opt-in Linux resource acceptance tests.
package testresource

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// RSS reads resident process memory, including native allocations, from procfs.
func RSS() (uint64, error) {
	status, err := os.ReadFile("/proc/self/status")
	if err != nil {
		return 0, err
	}
	return parseRSS(string(status))
}

func parseRSS(status string) (uint64, error) {
	for _, line := range strings.Split(status, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 || fields[0] != "VmRSS:" {
			continue
		}
		if len(fields) != 3 || fields[2] != "kB" {
			return 0, errors.New("unexpected VmRSS format")
		}
		value, err := strconv.ParseUint(fields[1], 10, 64)
		if err != nil || value > ^uint64(0)/1024 {
			return 0, errors.New("invalid VmRSS value")
		}
		return value * 1024, nil
	}
	return 0, errors.New("VmRSS is absent from procfs status")
}

type Measurement struct {
	Initial uint64
	Peak    uint64
	Final   uint64
	Samples int
}

// MeasureRSS samples while run executes. Peak is a sampled maximum, not VmHWM.
// The callback must return normally; failures should be returned rather than
// calling testing.Fatal inside it, so the sampling goroutine is always joined.
func MeasureRSS(interval time.Duration, run func() error) (Measurement, error) {
	if interval <= 0 {
		return Measurement{}, errors.New("RSS sampling interval must be positive")
	}
	initial, err := RSS()
	if err != nil {
		return Measurement{}, err
	}
	stop := make(chan struct{})
	type result struct {
		measurement Measurement
		err         error
	}
	finished := make(chan result, 1)
	go func() {
		value := Measurement{Initial: initial, Peak: initial, Final: initial, Samples: 1}
		var failure error
		sample := func() {
			rss, err := RSS()
			if err != nil {
				failure = errors.Join(failure, err)
				return
			}
			value.Final = rss
			value.Peak = max(value.Peak, rss)
			value.Samples++
		}
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				sample()
			case <-stop:
				sample()
				finished <- result{value, failure}
				return
			}
		}
	}()
	err = run()
	close(stop)
	measured := <-finished
	if measured.err != nil {
		return measured.measurement, errors.Join(err, fmt.Errorf("sample process RSS: %w", measured.err))
	}
	return measured.measurement, err
}
