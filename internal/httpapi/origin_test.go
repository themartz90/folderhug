package httpapi

import (
	"net/http"
	"testing"
)

func TestAutomaticAddressAcceptsIPWithoutWeakeningCSRF(t *testing.T) {
	for _, origin := range []string{"http://localhost:8080", "http://192.168.1.20:8080", "http://100.101.102.103:8080", "http://[fd00::20]:8080", "https://192.168.1.20"} {
		t.Run(origin, func(t *testing.T) {
			f := newAuthFixture(t, "http")
			f.server.Config.PublicURL = nil
			f.origin = origin
			cookie, session := f.login(t)
			if cookie.Secure != (origin[:5] == "https") {
				t.Fatal("wrong cookie transport policy")
			}
			requireStatus(t, f.request("GET", "/api/v1/auth/session", "", cookie, "", nil), 200)
			requireStatus(t, f.request("POST", "/api/v1/auth/logout", "", cookie, session.CSRFToken, map[string]string{"Origin": "https://attacker.example"}), 403)
			requireStatus(t, f.request("POST", "/api/v1/auth/logout", "", cookie, "wrong", nil), 403)
			requireStatus(t, f.request("POST", "/api/v1/auth/logout", "", cookie, session.CSRFToken, nil), 204)
		})
	}
}

func TestAutomaticAddressRejectsUnconfiguredDNSNamesAndProxyHeaders(t *testing.T) {
	f := newAuthFixture(t, "http")
	f.server.Config.PublicURL = nil
	f.origin = "http://192.168.1.20:8080"
	for _, host := range []string{"attacker.example:8080", "localhost.attacker.example", "0.0.0.0:8080", "[ff02::1]:8080", "user@localhost:8080", "localhost:99999"} {
		requireStatus(t, f.request("GET", "/api/v1/auth/setup", "", nil, "", map[string]string{"Host": host, "X-Forwarded-Host": "192.168.1.20:8080"}), http.StatusMisdirectedRequest)
	}
	w := f.request("POST", "/api/v1/auth/login", `{"password":"`+ownerPassword+`"}`, nil, "", map[string]string{"X-Forwarded-Proto": "https"})
	requireStatus(t, w, 200)
	if w.Result().Cookies()[0].Secure {
		t.Fatal("forwarded header changed cookie transport")
	}
	requireStatus(t, f.request("POST", "/api/v1/auth/login", `{"password":"`+ownerPassword+`"}`, nil, "", map[string]string{"Origin": "https://192.168.1.20:8080", "X-Forwarded-Proto": "https"}), 403)
}
