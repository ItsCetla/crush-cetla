package netlog

import (
	"io"
	"net/http"
	"sync"
)

// Transport is an http.RoundTripper that records every request in a
// ledger. It never buffers: request and response bodies are counted as
// they stream through, so SSE and websockets behave as before.
type Transport struct {
	// Base does the work. Nil means http.DefaultTransport.
	Base http.RoundTripper
	// Purpose labels every request this transport makes.
	Purpose Purpose
	// Ledger receives the counts. Nil means Default().
	Ledger *Ledger
}

// Wrap returns base wrapped to record into the default ledger under
// purpose. Wrapping a Transport again only relabels it, so nothing is
// counted twice.
func Wrap(base http.RoundTripper, purpose Purpose) http.RoundTripper {
	if t, ok := base.(*Transport); ok {
		return &Transport{Base: t.Base, Purpose: purpose, Ledger: t.Ledger}
	}
	return &Transport{Base: base, Purpose: purpose}
}

// Client returns a copy of c (or a fresh client when c is nil) whose
// transport records under purpose.
func Client(c *http.Client, purpose Purpose) *http.Client {
	var out http.Client
	if c != nil {
		out = *c
	}
	out.Transport = Wrap(out.Transport, purpose)
	return &out
}

var installOnce sync.Once

// InstallDefault makes http.DefaultClient record its requests as
// PurposeOther, so code that reaches for the default client still shows
// up on the receipt. http.DefaultTransport itself is left alone: the
// clients that clone it get their own wrapper.
func InstallDefault() {
	installOnce.Do(func() {
		http.DefaultClient.Transport = Wrap(http.DefaultClient.Transport, PurposeOther)
	})
}

// RoundTrip implements http.RoundTripper.
func (t *Transport) RoundTrip(req *http.Request) (*http.Response, error) {
	base := t.Base
	if base == nil {
		base = http.DefaultTransport
	}
	ledger := t.Ledger
	if ledger == nil {
		ledger = Default()
	}
	host := req.URL.Hostname()
	if host == "" {
		host = req.Host
	}
	e := ledger.row(SessionFrom(req.Context()), host, t.Purpose)
	e.requests.Add(1)
	e.out.Add(requestHeaderSize(req))
	e.touch(ledger.now())

	if req.Body != nil && req.Body != http.NoBody {
		// RoundTrippers must not modify the request, so send a shallow
		// copy with a counting body.
		r2 := new(http.Request)
		*r2 = *req
		r2.Body = &countingReader{rc: req.Body, n: &e.out}
		if req.GetBody != nil {
			getBody := req.GetBody
			r2.GetBody = func() (io.ReadCloser, error) {
				b, err := getBody()
				if err != nil || b == nil || b == http.NoBody {
					return b, err
				}
				return &countingReader{rc: b, n: &e.out}, nil
			}
		}
		req = r2
	}

	resp, err := base.RoundTrip(req)
	if err != nil || resp == nil {
		return resp, err
	}
	e.in.Add(responseHeaderSize(resp))
	e.touch(ledger.now())
	if resp.Body != nil && resp.Body != http.NoBody {
		cr := &countingReader{rc: resp.Body, n: &e.in, done: func() { e.touch(ledger.now()) }}
		if rwc, ok := resp.Body.(io.ReadWriteCloser); ok {
			// A 101 Switching Protocols body is the connection itself;
			// keep it writable.
			resp.Body = &countingConn{countingReader: cr, w: rwc, out: &e.out}
		} else {
			resp.Body = cr
		}
	}
	return resp, nil
}

type counter interface{ Add(int64) int64 }

// countingReader counts bytes read through it.
type countingReader struct {
	rc   io.ReadCloser
	n    counter
	done func()
	once sync.Once
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.rc.Read(p)
	if n > 0 {
		c.n.Add(int64(n))
	}
	if err != nil {
		c.finish()
	}
	return n, err
}

func (c *countingReader) Close() error {
	c.finish()
	return c.rc.Close()
}

func (c *countingReader) finish() {
	if c.done != nil {
		c.once.Do(c.done)
	}
}

// countingConn is a countingReader that also counts writes.
type countingConn struct {
	*countingReader
	w   io.Writer
	out counter
}

func (c *countingConn) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	if n > 0 {
		c.out.Add(int64(n))
	}
	return n, err
}

// requestHeaderSize estimates the request line and headers on the wire.
func requestHeaderSize(req *http.Request) int64 {
	n := len(req.Method) + len(req.URL.RequestURI()) + len(" HTTP/1.1\r\n")
	n += len("Host: \r\n") + len(req.Host)
	if req.Host == "" {
		n += len(req.URL.Host)
	}
	n += headerSize(req.Header) + 2
	return int64(n)
}

// responseHeaderSize estimates the status line and headers on the wire.
func responseHeaderSize(resp *http.Response) int64 {
	return int64(len(resp.Proto) + len(resp.Status) + 3 + headerSize(resp.Header) + 2)
}

func headerSize(h http.Header) int {
	n := 0
	for k, vs := range h {
		for _, v := range vs {
			n += len(k) + len(v) + 4
		}
	}
	return n
}
