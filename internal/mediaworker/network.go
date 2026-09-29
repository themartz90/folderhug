package mediaworker

import (
	"context"
	"errors"
	"net"
	"syscall"
	"time"
)

// NetworkCheck is a staging diagnostic. Refused connections count as reachable;
// only a timeout or an explicit missing route supports the isolation check.
func NetworkCheck(ctx context.Context) error {
	for _, destination := range []string{"1.1.1.1:443", "172.17.0.1:443", "10.0.0.1:443"} {
		connection, err := (&net.Dialer{Timeout: 400 * time.Millisecond}).DialContext(ctx, "tcp", destination)
		if err == nil {
			connection.Close()
			return errors.New("media worker network isolation check failed")
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		var netError net.Error
		if errors.Is(err, syscall.ENETUNREACH) || errors.Is(err, syscall.EHOSTUNREACH) || errors.As(err, &netError) && netError.Timeout() {
			continue
		}
		return errors.New("media worker network isolation could not be confirmed")
	}
	return nil
}
