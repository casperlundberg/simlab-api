package api_test

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/casperlundberg/simlab-api/internal/api"
	"github.com/casperlundberg/simlab-api/internal/autoscaler"
)

// These need no database: the token is checked before a handler runs, so a
// refused request never reaches the store. That is also the property worth
// having — an unauthenticated caller must not be able to make this service do
// any work at all.

func handlerWithToken(t *testing.T, token string) http.Handler {
	t.Helper()
	// An autoscaler client pointed at a closed port. The guarded handlers
	// behind it will fail, which is fine and is the point: what is under test
	// is whether the request was admitted at all, and a request that reaches a
	// handler and fails there was admitted. Without this they dereference nil
	// and the panic is indistinguishable from a refusal.
	client, err := autoscaler.New("http://127.0.0.1:1", "", time.Second)
	if err != nil {
		t.Fatalf("building the autoscaler client: %v", err)
	}
	return api.New(api.Options{Token: token, Autoscaler: client})
}

func TestAnUnauthenticatedRequestToTheAPIIsRefused(t *testing.T) {
	server := httptest.NewServer(handlerWithToken(t, "correct-horse"))
	defer server.Close()

	// Every shape of API call, not just a read: this service proxies to the
	// autoscaler, so an open write here reaches an authenticated autoscaler.
	for _, call := range []struct{ method, path string }{
		{http.MethodGet, "/api/mines"},
		{http.MethodPost, "/api/mines"},
		{http.MethodDelete, "/api/mines/anything"},
		{http.MethodGet, "/api/runs"},
		{http.MethodPost, "/api/runs"},
		{http.MethodGet, "/api/targets"},
		{http.MethodPost, "/api/targets"},
		{http.MethodGet, "/api/platforms"},
		{http.MethodGet, "/api/events"},
	} {
		request, err := http.NewRequest(call.method, server.URL+call.path, nil)
		if err != nil {
			t.Fatalf("building %s %s: %v", call.method, call.path, err)
		}
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatalf("%s %s: %v", call.method, call.path, err)
		}
		response.Body.Close()
		if response.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s %s without a token: got %d, want 401",
				call.method, call.path, response.StatusCode)
		}
	}
}

func TestTheProbesStayOpenSoKubernetesCanUseThem(t *testing.T) {
	server := httptest.NewServer(handlerWithToken(t, "correct-horse"))
	defer server.Close()

	// A kubelet cannot hold a credential: guarding these would fail every
	// readiness check and the Deployment would never become available.
	//
	// /healthz only here — /readyz reports on the database, which these tests
	// deliberately do without. Both are declared Public in the same table.
	response, err := http.Get(server.URL + "/healthz")
	if err != nil {
		t.Fatalf("GET /healthz: %v", err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Errorf("GET /healthz: got %d, want 200 — probes must stay reachable",
			response.StatusCode)
	}
}

func TestTheConfiguredTokenIsAccepted(t *testing.T) {
	server := httptest.NewServer(handlerWithToken(t, "correct-horse"))
	defer server.Close()

	request, _ := http.NewRequest(http.MethodGet, server.URL+"/api/platforms", nil)
	request.Header.Set("Authorization", "Bearer correct-horse")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("GET /api/platforms: %v", err)
	}
	response.Body.Close()
	if response.StatusCode == http.StatusUnauthorized {
		t.Error("the configured token was refused")
	}
}

func TestAnythingOtherThanTheTokenIsRefused(t *testing.T) {
	server := httptest.NewServer(handlerWithToken(t, "correct-horse"))
	defer server.Close()

	for _, header := range []string{
		"Bearer wrong-horse",
		"Bearer correct-horse-and-more", // a prefix of the token is not the token
		"Bearer correct-hors",           // nor is the token missing its last byte
		"Bearer ",
		"correct-horse", // the scheme is not optional
		"Basic Y29ycmVjdC1ob3JzZQ==",
		"",
	} {
		request, _ := http.NewRequest(http.MethodGet, server.URL+"/api/mines", nil)
		if header != "" {
			request.Header.Set("Authorization", header)
		}
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatalf("GET /api/mines with %q: %v", header, err)
		}
		response.Body.Close()
		if response.StatusCode != http.StatusUnauthorized {
			t.Errorf("Authorization %q: got %d, want 401", header, response.StatusCode)
		}
	}
}

func TestTheEventStreamAcceptsTheTokenInTheQueryString(t *testing.T) {
	server := httptest.NewServer(handlerWithToken(t, "correct-horse"))
	defer server.Close()

	// EventSource cannot set headers, so the streams would otherwise have to be
	// left open. The check is shared by every route, so it is asserted on a
	// read that returns rather than on /api/events, which by design does not.
	response, err := http.Get(server.URL + "/api/platforms?token=correct-horse")
	if err != nil {
		t.Fatalf("GET /api/platforms: %v", err)
	}
	response.Body.Close()
	if response.StatusCode == http.StatusUnauthorized {
		t.Error("a correct token in the query string was refused on a read")
	}

	response, err = http.Get(server.URL + "/api/platforms?token=wrong-horse")
	if err != nil {
		t.Fatalf("GET /api/platforms: %v", err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized {
		t.Errorf("a wrong token in the query string: got %d, want 401", response.StatusCode)
	}
}

func TestAQueryTokenCannotAuthoriseAWrite(t *testing.T) {
	server := httptest.NewServer(handlerWithToken(t, "correct-horse"))
	defer server.Close()

	// A URL ends up in access logs, history and referrers. It may let someone
	// read; it must never let them change anything.
	// Real routes: the mux answers 405 for a method it does not serve, before
	// the guard is ever consulted, which would prove nothing about the guard.
	for _, call := range []struct{ method, path string }{
		{http.MethodPost, "/api/mines"},
		{http.MethodPut, "/api/mines/x"},
		{http.MethodDelete, "/api/mines/x"},
		{http.MethodPost, "/api/runs"},
		{http.MethodPost, "/api/targets"},
	} {
		request, _ := http.NewRequest(call.method, server.URL+call.path+"?token=correct-horse", nil)
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatalf("%s %s: %v", call.method, call.path, err)
		}
		response.Body.Close()
		if response.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s %s authorised by a query token: got %d, want 401",
				call.method, call.path, response.StatusCode)
		}
	}
}

func TestAnEmptyTokenLeavesTheAPIOpen(t *testing.T) {
	server := httptest.NewServer(handlerWithToken(t, ""))
	defer server.Close()

	// Deliberate, and the same choice the autoscaler makes: a development
	// install with no token configured has to work. Every chart that exposes
	// this service sets one, and the umbrella chart refuses to render if the
	// autoscaler is guarded and this is not.
	response, err := http.Get(server.URL + "/api/platforms")
	if err != nil {
		t.Fatalf("GET /api/platforms: %v", err)
	}
	response.Body.Close()
	if response.StatusCode == http.StatusUnauthorized {
		t.Error("no token is configured, so nothing should be refused")
	}
}
