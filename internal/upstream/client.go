package upstream

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"sync"
	"time"
)

var (
	ErrPolicy        = errors.New("upstream destination is not allowed")
	ErrNetwork       = errors.New("upstream connection failed")
	ErrTimeout       = errors.New("upstream request timed out")
	ErrMethod        = errors.New("upstream method is not allowed")
	ErrRedirect      = errors.New("upstream redirect limit exceeded")
	ErrResponseLimit = errors.New("upstream response exceeded its byte budget")
)

type Credentials struct{ Username, Password string }

type Options struct {
	Credentials *Credentials
	Headers     http.Header
	// Zero uses 8 MiB and 30 seconds. Callers choose finite transfer/job budgets.
	MaxBytes int64
	Timeout  time.Duration
	// Interactive reserves capacity for browsing and connection diagnostics.
	Interactive bool
}

type Client struct {
	policy     Policy
	transport  *http.Transport
	lookupIP   func(context.Context, string) ([]netip.Addr, error)
	dialIP     func(context.Context, string, string) (net.Conn, error)
	mu         sync.Mutex
	active     int
	background int
	origins    map[string]originUse
	changed    chan struct{}
}

type originUse struct{ active, background int }

func New(policy Policy) *Client {
	policy = copyPolicy(policy)
	dialer := &net.Dialer{Timeout: policy.ConnectTimeout, KeepAlive: 30 * time.Second}
	c := &Client{
		policy: policy,
		lookupIP: func(ctx context.Context, host string) ([]netip.Addr, error) {
			return net.DefaultResolver.LookupNetIP(ctx, "ip", host)
		},
		dialIP:  dialer.DialContext,
		origins: make(map[string]originUse), changed: make(chan struct{}),
	}
	c.transport = &http.Transport{
		Proxy: nil, DialContext: c.dialContext,
		TLSClientConfig:        &tls.Config{MinVersion: tls.VersionTLS12},
		TLSHandshakeTimeout:    policy.ConnectTimeout,
		ResponseHeaderTimeout:  policy.HeaderTimeout,
		MaxResponseHeaderBytes: 64 << 10,
		IdleConnTimeout:        90 * time.Second,
		MaxIdleConns:           policy.MaxConcurrent,
		MaxIdleConnsPerHost:    policy.PerOrigin,
		MaxConnsPerHost:        policy.PerOrigin,
		DisableCompression:     true,
		ForceAttemptHTTP2:      true,
	}
	return c
}

func (c *Client) CloseIdleConnections() { c.transport.CloseIdleConnections() }

// Do performs a root-relative GET or HEAD. The caller must close every returned
// body. Closing, EOF, or cancellation releases the shared concurrency slot.
// Request headers are allowlisted; credentials are set only after URL checks.
func (c *Client) Do(ctx context.Context, root *Root, method, relativePath string, options Options) (*http.Response, error) {
	if root == nil {
		return nil, ErrURL
	}
	if method != http.MethodGet && method != http.MethodHead {
		return nil, ErrMethod
	}
	if options.MaxBytes < 0 || options.Timeout < 0 {
		return nil, ErrPolicy
	}
	if options.MaxBytes == 0 {
		options.MaxBytes = 8 << 20
	}
	if options.Timeout == 0 {
		options.Timeout = 30 * time.Second
	}
	target, err := root.Resolve(relativePath)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, options.Timeout)
	release, err := c.acquire(ctx, target.Scheme+"://"+target.Host, options.Interactive)
	if err != nil {
		cancel()
		return nil, err
	}
	var once sync.Once
	finish := func() { once.Do(func() { cancel(); release() }) }
	for redirects := 0; ; redirects++ {
		req, err := http.NewRequestWithContext(ctx, method, target.String(), nil)
		if err != nil {
			finish()
			return nil, ErrURL
		}
		for _, name := range []string{"Accept", "Range", "If-Range", "If-Match", "If-None-Match", "If-Modified-Since", "If-Unmodified-Since", "User-Agent", "X-GitHub-Api-Version"} {
			if value := options.Headers.Get(name); value != "" {
				req.Header.Set(name, value)
			}
		}
		req.Header.Set("Accept-Encoding", "identity")
		if options.Credentials != nil {
			req.SetBasicAuth(options.Credentials.Username, options.Credentials.Password)
		}
		res, err := c.transport.RoundTrip(req)
		if err != nil {
			result := safeError(ctx, err)
			finish()
			return nil, result
		}
		if isRedirect(res.StatusCode) && res.Header.Get("Location") != "" {
			res.Body.Close()
			if redirects == 3 {
				finish()
				return nil, ErrRedirect
			}
			target, err = root.Child(target, res.Header.Get("Location"))
			if err != nil {
				finish()
				return nil, err
			}
			continue
		}
		if method != http.MethodHead && res.ContentLength > options.MaxBytes {
			res.Body.Close()
			finish()
			return nil, ErrResponseLimit
		}
		body := &boundedBody{body: res.Body, remaining: options.MaxBytes, ctx: ctx, finish: finish}
		context.AfterFunc(ctx, func() { body.Close() })
		res.Body = body
		return res, nil
	}
}

func isRedirect(status int) bool {
	return status == 301 || status == 302 || status == 303 || status == 307 || status == 308
}

func safeError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	for _, safe := range []error{ErrPolicy, ErrNetwork, ErrTimeout, ErrResponseLimit} {
		if errors.Is(err, safe) {
			return safe
		}
	}
	var timeout net.Error
	if errors.As(err, &timeout) && timeout.Timeout() {
		return ErrTimeout
	}
	return ErrNetwork
}

func (c *Client) acquire(ctx context.Context, origin string, interactive bool) (func(), error) {
	for {
		c.mu.Lock()
		use := c.origins[origin]
		globalBackground := max(1, c.policy.MaxConcurrent-c.policy.ReservedInteractive)
		originBackground := max(1, c.policy.PerOrigin-c.policy.ReservedInteractive)
		available := c.active < c.policy.MaxConcurrent && use.active < c.policy.PerOrigin
		if !interactive {
			available = available && c.background < globalBackground && use.background < originBackground
		}
		if available {
			c.active++
			use.active++
			if !interactive {
				c.background++
				use.background++
			}
			c.origins[origin] = use
			c.mu.Unlock()
			var once sync.Once
			return func() {
				once.Do(func() {
					c.mu.Lock()
					defer c.mu.Unlock()
					use := c.origins[origin]
					c.active--
					use.active--
					if !interactive {
						c.background--
						use.background--
					}
					if use.active == 0 {
						delete(c.origins, origin)
					} else {
						c.origins[origin] = use
					}
					close(c.changed)
					c.changed = make(chan struct{})
				})
			}, nil
		}
		changed := c.changed
		c.mu.Unlock()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-changed:
		}
	}
}

type boundedBody struct {
	body      io.ReadCloser
	remaining int64
	ctx       context.Context
	finish    func()
	once      sync.Once
	terminal  error
}

func (b *boundedBody) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if b.terminal != nil {
		return 0, b.terminal
	}
	if b.ctx.Err() != nil {
		return 0, b.ctx.Err()
	}
	// One lookahead byte distinguishes an exact-size body from an overrun.
	if b.remaining == 0 {
		var extra [1]byte
		n, err := b.body.Read(extra[:])
		if n != 0 {
			b.terminal = ErrResponseLimit
			b.Close()
			return 0, ErrResponseLimit
		}
		if err != nil {
			result := err
			if err != io.EOF {
				result = safeError(b.ctx, err)
			}
			b.terminal = result
			b.Close()
			return 0, result
		}
		return 0, nil
	}
	n, err := b.body.Read(p[:min(int64(len(p)), b.remaining)])
	b.remaining -= int64(n)
	if err != nil {
		result := err
		if err != io.EOF {
			result = safeError(b.ctx, err)
		}
		b.terminal = result
		b.Close()
		return n, result
	}
	return n, nil
}

func (b *boundedBody) Close() error {
	b.once.Do(func() {
		b.body.Close()
		b.finish()
	})
	return nil
}
