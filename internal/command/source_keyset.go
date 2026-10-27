package command

import (
	"context"
	"encoding/json/v2"
	"flag"
	"fmt"
	"io"

	"github.com/dsolsona/almanac/internal/sourcesecret"
)

type sourceKeysetResult struct {
	SchemaVersion int    `json:"schema_version"`
	Status        string `json:"status"`
	KeysetID      string `json:"keyset_id"`
}

func secretsCommand(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] != "provision-source-keyset" {
		fmt.Fprintln(stderr, "secrets: usage: almanac secrets provision-source-keyset --file=FILE --keyset-id=UUID [--output=json]")
		return 2
	}
	fs := flag.NewFlagSet("secrets provision-source-keyset", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	path := fs.String("file", "", "protected output file")
	keysetID := fs.String("keyset-id", "", "canonical keyset UUID")
	output := fs.String("output", "text", "text or json")
	if err := fs.Parse(args[1:]); err != nil || fs.NArg() != 0 || *path == "" || *keysetID == "" || (*output != "text" && *output != "json") {
		fmt.Fprintln(stderr, "secrets: usage: almanac secrets provision-source-keyset --file=FILE --keyset-id=UUID [--output=json]")
		return 2
	}
	if err := ctx.Err(); err != nil {
		fmt.Fprintln(stderr, "secrets: provisioning failed")
		return 1
	}
	if err := sourcesecret.Provision(*path, *keysetID); err != nil {
		fmt.Fprintln(stderr, "secrets: source keyset provisioning failed")
		return 1
	}
	if *output == "json" {
		result := sourceKeysetResult{SchemaVersion: 1, Status: "provisioned", KeysetID: *keysetID}
		if err := json.MarshalWrite(stdout, result); err != nil {
			fmt.Fprintln(stderr, "secrets: output failed")
			return 1
		}
		fmt.Fprintln(stdout)
		return 0
	}
	fmt.Fprintf(stdout, "Source-secret keyset provisioned. Keyset ID: %s\n", *keysetID)
	return 0
}
