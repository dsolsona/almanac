package datasourcehttp

import (
	"net/http"
	"testing"
)

func TestDatasourceETagRequiresOneCanonicalStrongVersion(t *testing.T) {
	const id = "123e4567-e89b-42d3-a456-426614174000"
	valid := datasourceETag(id, 12)
	if !singleETag([]string{valid}, id) {
		t.Fatalf("singleETag(%q) = false, want true", valid)
	}
	for _, values := range [][]string{
		{`W/"datasource:` + id + `:12"`},
		{`"datasource:` + id + `:012"`},
		{`"datasource:` + id + `:12", "datasource:` + id + `:12"`},
		{`"datasource:` + id + `:12"`, `"datasource:` + id + `:13"`},
		{`"datasource:123e4567-e89b-42d3-a456-426614174001:12"`},
		{`"datasource:` + id + `:0"`},
	} {
		if singleETag(values, id) {
			t.Errorf("singleETag(%q) = true, want false", values)
		}
	}
}

func TestMutationHeadersRejectDuplicateIdempotencyKey(t *testing.T) {
	request := &http.Request{Header: make(http.Header)}
	request.Header.Add("Idempotency-Key", "first")
	request.Header.Add("Idempotency-Key", "second")
	if singlePrintableHeader(request.Header, "Idempotency-Key", 1, 128) {
		t.Fatal("duplicate Idempotency-Key accepted")
	}
	request.Header = make(http.Header)
	request.Header.Set("Idempotency-Key", "safe-123")
	if !singlePrintableHeader(request.Header, "Idempotency-Key", 1, 128) {
		t.Fatal("valid Idempotency-Key rejected")
	}
}
