package upstream

import (
	"errors"
	"net"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"unicode/utf8"

	"golang.org/x/net/idna"
)

var ErrURL = errors.New("upstream URL is not allowed")

// Root retains a validated origin and directory boundary. Returned URLs are copies.
type Root struct{ base url.URL }

func NewRoot(raw string, allowHTTP bool) (*Root, error) {
	u, err := url.Parse(raw)
	if err != nil || strings.Contains(raw, "#") || len(raw) > 4096 {
		return nil, ErrURL
	}
	if u.Scheme != "https" && !(u.Scheme == "http" && allowHTTP) {
		return nil, ErrURL
	}
	if err := normalizeURL(u); err != nil {
		return nil, err
	}
	if u.Path == "" {
		u.Path = "/"
	}
	return &Root{base: *u}, nil
}

func (r *Root) URL() *url.URL { u := r.base; return &u }

// Resolve accepts only an escaped path relative to this root, with no query.
// The empty path addresses the configured root, including its original slash.
func (r *Root) Resolve(relative string) (*url.URL, error) {
	if relative == "" {
		return r.URL(), nil
	}
	if strings.HasPrefix(relative, "/") || strings.ContainsAny(relative, "?#") {
		return nil, ErrURL
	}
	u, err := url.Parse(relative)
	if err != nil || u.IsAbs() || u.Host != "" || u.Opaque != "" || u.User != nil {
		return nil, ErrURL
	}
	if err := validatePath(u.EscapedPath()); err != nil {
		return nil, err
	}
	target := r.URL()
	escaped := strings.TrimSuffix(target.EscapedPath(), "/") + "/" + u.EscapedPath()
	if err := validatePath(escaped); err != nil {
		return nil, err
	}
	target.Path, _ = url.PathUnescape(escaped)
	target.RawPath = escaped
	return target, nil
}

// Child resolves a listing link or redirect, rejecting parent and sort links.
// The supplied current URL must already lie within this root.
func (r *Root) Child(current *url.URL, href string) (*url.URL, error) {
	if current == nil || strings.ContainsAny(href, "?#") || len(href) > 4096 {
		return nil, ErrURL
	}
	if _, err := r.Relative(current); err != nil {
		return nil, err
	}
	ref, err := url.Parse(href)
	if err != nil || ref.User != nil || ref.Opaque != "" {
		return nil, ErrURL
	}
	if err := validatePath(ref.EscapedPath()); err != nil {
		return nil, err
	}
	target := current.ResolveReference(ref)
	if _, err := r.Relative(target); err != nil {
		return nil, err
	}
	if err := normalizeURL(target); err != nil {
		return nil, err
	}
	return target, nil
}

// Relative validates origin and path boundaries before returning an escaped path.
func (r *Root) Relative(target *url.URL) (string, error) {
	if target == nil {
		return "", ErrURL
	}
	u := *target
	if err := normalizeURL(&u); err != nil {
		return "", err
	}
	if u.Scheme != r.base.Scheme || u.Host != r.base.Host {
		return "", ErrURL
	}
	boundary := strings.TrimSuffix(r.base.Path, "/")
	if u.Path != boundary && !strings.HasPrefix(u.Path, boundary+"/") {
		return "", ErrURL
	}
	if boundary == "" {
		return strings.TrimPrefix(u.EscapedPath(), "/"), nil
	}
	parts := strings.Split(strings.TrimPrefix(u.EscapedPath(), "/"), "/")
	depth := len(strings.Split(strings.TrimPrefix(boundary, "/"), "/"))
	return strings.Join(parts[depth:], "/"), nil
}

func normalizeURL(u *url.URL) error {
	if u.Scheme != "https" && u.Scheme != "http" {
		return ErrURL
	}
	if u.Host == "" || u.User != nil || u.Opaque != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.RawFragment != "" {
		return ErrURL
	}
	if strings.ContainsAny(u.Host, "%\\") || strings.HasSuffix(u.Host, ":") {
		return ErrURL
	}
	host := strings.ToLower(u.Hostname())
	if addr, err := netip.ParseAddr(host); err == nil {
		host = addr.String()
	} else {
		var err error
		host, err = idna.Lookup.ToASCII(host)
		if err != nil || host == "" || strings.Contains(host, ":") {
			return ErrURL
		}
		host = strings.TrimSuffix(host, ".")
		for _, label := range strings.Split(host, ".") {
			if label == "" || len(label) > 63 || strings.HasPrefix(label, "-") || strings.HasSuffix(label, "-") {
				return ErrURL
			}
			for _, ch := range label {
				if (ch < 'a' || ch > 'z') && (ch < '0' || ch > '9') && ch != '-' {
					return ErrURL
				}
			}
		}
	}
	port := u.Port()
	if port != "" {
		n, err := strconv.ParseUint(port, 10, 16)
		if err != nil || n == 0 {
			return ErrURL
		}
		port = strconv.FormatUint(n, 10)
	}
	if (u.Scheme == "https" && port == "443") || (u.Scheme == "http" && port == "80") {
		port = ""
	}
	if port != "" {
		u.Host = net.JoinHostPort(host, port)
	} else if strings.Contains(host, ":") {
		u.Host = "[" + host + "]"
	} else {
		u.Host = host
	}
	return validatePath(u.EscapedPath())
}

func validatePath(escaped string) error {
	if len(escaped) > 4096 || strings.Contains(escaped, "//") {
		return ErrURL
	}
	for _, segment := range strings.Split(escaped, "/") {
		decoded, err := url.PathUnescape(segment)
		if err != nil || !utf8.ValidString(decoded) || decoded == "." || decoded == ".." {
			return ErrURL
		}
		for _, ch := range decoded {
			if ch < 32 || ch == 127 || ch == '/' || ch == '\\' {
				return ErrURL
			}
		}
		// A literal percent is allowed. Escapes that could acquire structural
		// meaning after another decoding pass are deliberately unsupported.
		lower := strings.ToLower(decoded)
		for _, ambiguous := range []string{"%2e", "%2f", "%5c", "%00", "%25"} {
			if strings.Contains(lower, ambiguous) {
				return ErrURL
			}
		}
	}
	return nil
}
