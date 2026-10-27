package command

import "testing"

func TestDatasourceOperationDispatch(t *testing.T) {
	tests := []struct {
		args      []string
		operation string
		rest      int
	}{
		{[]string{"datasources", "list", "--profile-file=x"}, "datasources list", 1},
		{[]string{"datasources", "create"}, "datasources create", 0},
		{[]string{"datasources", "update"}, "datasources update", 0},
		{[]string{"datasources", "test"}, "datasources test", 0},
		{[]string{"providers", "list"}, "providers list", 0},
		{[]string{"slis", "test"}, "slis test", 0},
		{[]string{"datasources", "delete"}, "", 2},
	}
	for _, test := range tests {
		operation, rest := datasourceOperation(test.args)
		if operation != test.operation || len(rest) != test.rest {
			t.Fatalf("dispatch %q = %q with %d args", test.args, operation, len(rest))
		}
	}
}

func TestDatasourceETagRequiresCanonicalResourceVersion(t *testing.T) {
	id := "8ed93d7e-51e3-4a3e-9222-3e8a612de20a"
	if !validDatasourceETag(`"datasource:`+id+`:7"`, id) {
		t.Fatal("valid datasource ETag was rejected")
	}
	for _, etag := range []string{`W/"datasource:` + id + `:7"`, `"datasource:` + id + `:0"`, `"datasource:` + id + `:07"`, `"datasource:` + id + `:7,8"`} {
		if validDatasourceETag(etag, id) {
			t.Fatalf("accepted invalid ETag %q", etag)
		}
	}
}
