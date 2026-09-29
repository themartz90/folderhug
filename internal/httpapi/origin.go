package httpapi

import (
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
)

// Automatic addresses allow direct IP access without trusting arbitrary DNS
// names or forwarded headers. Custom hostnames use an explicit public URL.
func (s *Server) requestOrigin(r *http.Request) (string, bool) {
	if s.Config.PublicURL != nil {
		return s.Config.PublicURL.String(), r.Host == s.Config.PublicURL.Host
	}
	u, err := url.Parse("http://" + r.Host)
	if err != nil || u.Host == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
		return "", false
	}
	if port := u.Port(); port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return "", false
		}
	}
	if !strings.EqualFold(u.Hostname(), "localhost") {
		ip, err := netip.ParseAddr(u.Hostname())
		if err != nil || ip.IsUnspecified() || ip.IsMulticast() || ip.Zone() != "" {
			return "", false
		}
	}
	if r.TLS != nil {
		u.Scheme = "https"
	}
	return u.String(), true
}

func (s *Server) secureCookie(r *http.Request) bool {
	if s.Config.PublicURL != nil {
		return s.Config.PublicURL.Scheme == "https"
	}
	return r.TLS != nil
}
