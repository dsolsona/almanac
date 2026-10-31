package agentws

import (
	"bufio"
	"net"
	"net/http"
)

// Keep the accepted transport so shutdown can interrupt a close handshake.
// websocket.CloseNow waits when another goroutine has already begun Close;
// closing the underlying socket is what actually interrupts that handshake.
type upgradeCapture struct {
	http.ResponseWriter
	transport net.Conn
}

func (w *upgradeCapture) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *upgradeCapture) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	conn, buffer, err := http.NewResponseController(w.ResponseWriter).Hijack()
	if err == nil {
		w.transport = conn
	}
	return conn, buffer, err
}
