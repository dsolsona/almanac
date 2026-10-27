package prometheus

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSafeDialFallsBackAcrossAllowedLoopbackAddresses(t *testing.T) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	address := fmt.Sprintf("localhost:%d", listener.Addr().(*net.TCPAddr).Port)
	connection, err := safeDialContext(context.Background(), "tcp", address, []string{"localhost"})
	if err != nil {
		t.Fatalf("dial reachable allowed localhost address: %v", err)
	}
	_ = connection.Close()
}

func TestQueryPreviewUsesConfiguredBearerAndReturnsBoundedVector(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/query" || r.URL.Query().Get("query") != "up" {
			t.Errorf("request = %s %s", r.Method, r.URL.String())
		}
		if r.Header.Get("Authorization") != "Bearer preview-secret" {
			t.Errorf("authorization = %q", r.Header.Get("Authorization"))
		}
		_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":[{"metric":{"job":"api"},"value":[1760000000,"1"]}]}}`))
	}))
	defer server.Close()

	client, err := NewClient(Config{Endpoint: server.URL, AuthMode: "bearer", BearerToken: "preview-secret", AllowedDestinations: []string{"127.0.0.1"}})
	if err != nil {
		t.Fatal(err)
	}
	result, err := client.Query(context.Background(), "up")
	if err != nil {
		t.Fatal(err)
	}
	if result.Class != "complete" || len(result.Series) != 1 || result.Series[0].Value != "1" {
		t.Fatalf("result = %#v", result)
	}
	if strings.Contains(result.Message, "preview-secret") {
		t.Fatal("result disclosed configured credentials")
	}
}

func TestQueryPreviewDoesNotFollowRedirect(t *testing.T) {
	redirected := false
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { redirected = true }))
	defer target.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Redirect(w, &http.Request{}, target.URL, http.StatusFound)
	}))
	defer source.Close()
	client, err := NewClient(Config{Endpoint: source.URL, AllowedDestinations: []string{"127.0.0.1"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Query(context.Background(), "up"); err == nil {
		t.Fatal("redirect response was accepted")
	}
	if redirected {
		t.Fatal("request followed a redirect to a different origin")
	}
}

func TestQueryPreviewRequiresOperatorDestinationAllowlist(t *testing.T) {
	hit := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hit = true
		_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":[]}}`))
	}))
	defer server.Close()
	client, err := NewClient(Config{Endpoint: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Query(context.Background(), "up")
	var sourceError *SourceError
	if !errors.As(err, &sourceError) || sourceError.Code != "network" {
		t.Fatalf("Query error = %v, want safe network failure", err)
	}
	if hit {
		t.Fatal("unlisted destination received a request")
	}
}

func TestMetadataDestinationsAreDenied(t *testing.T) {
	for _, address := range []string{"169.254.169.254", "169.254.170.2", "100.100.100.200", "fd00:ec2::254"} {
		if !deniedDestination(net.ParseIP(address)) {
			t.Errorf("metadata destination %s was allowed", address)
		}
	}
}

func TestQueryPreviewClassifiesInvalidPromQLWithoutReturningSourceBody(t *testing.T) {
	const sensitiveError = "upstream included details"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, sensitiveError, http.StatusBadRequest)
	}))
	defer server.Close()
	client, err := NewClient(Config{Endpoint: server.URL, AllowedDestinations: []string{"127.0.0.1"}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Query(context.Background(), "bad(")
	var sourceError *SourceError
	if !errors.As(err, &sourceError) || sourceError.Code != "query" || strings.Contains(err.Error(), sensitiveError) {
		t.Fatalf("Query error = %v, want redacted query classification", err)
	}
}
