package agentcmd

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"sync"
	"time"

	"github.com/dsolsona/almanac/internal/agentprotocol"
)

const diagnosticUploadWorkers = maxActiveDiagnostics

type diagnosticUpload struct {
	result     agentprotocol.DiagnosticResult
	deadlineAt time.Time
	body       []byte
}

func (c *client) startDiagnosticUploads(ctx context.Context, workers *sync.WaitGroup) {
	if c.diagnosticUploads == nil {
		c.diagnosticUploads = make(chan diagnosticUpload, maxActiveDiagnostics)
	}
	for range diagnosticUploadWorkers {
		workers.Go(func() {
			for {
				select {
				case <-ctx.Done():
					return
				case upload := <-c.diagnosticUploads:
					if !c.uploadDiagnostic(ctx, upload) {
						diagnosticTransferFailure()
					}
				}
			}
		})
	}
}

func (c *client) queueDiagnosticUpload(ctx context.Context, completed completedDiagnosticResult, body []byte) bool {
	if len(body) <= agentprotocol.MaxInlineDiagnosticResultBytes || len(body) > agentprotocol.MaxHTTPSDiagnosticResultBytes || !completed.deadlineAt.After(time.Now()) {
		return false
	}
	select {
	case <-ctx.Done():
		return false
	case c.diagnosticUploads <- diagnosticUpload{result: completed.result, deadlineAt: completed.deadlineAt, body: body}:
		return true
	default:
		return false
	}
}

func (c *client) uploadDiagnostic(parent context.Context, upload diagnosticUpload) bool {
	if c == nil || c.http == nil || c.opts.backendURL == "" || c.generation == "" || len(upload.body) <= agentprotocol.MaxInlineDiagnosticResultBytes || len(upload.body) > agentprotocol.MaxHTTPSDiagnosticResultBytes {
		return false
	}
	deadline := upload.deadlineAt
	if parentDeadline, ok := parent.Deadline(); ok && parentDeadline.Before(deadline) {
		deadline = parentDeadline
	}
	ctx, cancel := context.WithDeadline(parent, deadline)
	defer cancel()
	u, err := url.Parse(c.opts.backendURL)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil {
		return false
	}
	u.Path = "/api/agent/v1/diagnostics/" + upload.result.DiagnosticID + "/result"
	backoff := 100 * time.Millisecond
	for ctx.Err() == nil {
		requestCtx, cancelRequest := context.WithTimeout(ctx, 5*time.Second)
		request, requestErr := http.NewRequestWithContext(requestCtx, http.MethodPost, u.String(), bytes.NewReader(upload.body))
		if requestErr != nil {
			cancelRequest()
			return false
		}
		request.Header.Set("Authorization", "Bearer "+c.token)
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("X-Agent-Session-Generation", c.generation)
		response, requestErr := c.http.Do(request)
		if requestErr == nil {
			_ = response.Body.Close()
			cancelRequest()
			if response.StatusCode == http.StatusNoContent {
				return true
			}
			if response.StatusCode != http.StatusTooManyRequests && response.StatusCode < http.StatusInternalServerError {
				return false
			}
		} else {
			cancelRequest()
		}
		timer := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			timer.Stop()
			return false
		case <-timer.C:
		}
		if backoff < time.Second {
			backoff *= 2
		}
	}
	return false
}

func diagnosticTransferFailure() {
	_, _ = fmt.Fprintln(os.Stderr, "agent diagnostic result transfer could not be confirmed before its deadline")
}
