package client

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// counted is a canned server answering each request with the next status in
// turn (the last one from then on), counting the requests it saw.
type counted struct {
	mu       sync.Mutex
	statuses []int
	seen     int
}

func (c *counted) handler(w http.ResponseWriter, _ *http.Request) {
	c.mu.Lock()
	status := c.statuses[min(c.seen, len(c.statuses)-1)]
	c.seen++
	c.mu.Unlock()
	if status == 0 || status == cutShort {
		// the connection dropped before an answer, as a proxy restarting
		// does, or part way through one
		hj, ok := w.(http.Hijacker)
		if !ok {
			panic("the test server cannot drop a connection")
		}
		conn, _, err := hj.Hijack()
		if err != nil {
			panic(err)
		}
		if status == cutShort {
			if _, err := io.WriteString(conn, "HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: 100\r\n\r\n{\"Items\":["); err != nil {
				panic(err)
			}
		}
		if err := conn.Close(); err != nil {
			panic(err)
		}

		return
	}
	w.WriteHeader(status)
	if _, err := io.WriteString(w, http.StatusText(status)); err != nil {
		panic(err)
	}
}

func (c *counted) requests() int {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.seen
}

// cutShort answers a request with a 200 whose body the connection drops
// part way through.
const cutShort = -1

// retrying is a client for a counted server that tries a read four times,
// without waiting between.
func retrying(t *testing.T, statuses ...int) (*Client, *counted) {
	t.Helper()

	s := &counted{statuses: statuses}
	c := serve(t, EmbyToken(testToken), s.handler)
	c.Retry = Retry{Tries: 4, Wait: func(int) time.Duration { return time.Millisecond }}

	return c, s
}

// A read the server, or a proxy in front of it, answers as too busy or could
// not reach it is sent again, a bounded number of times: one gateway answer
// in the middle of a sweep of thousands of reads failed the whole sweep.
func TestReadsAreRetriedAfterATransientFailure(t *testing.T) {
	t.Parallel()

	get := RequestOptions{HTTPMethod: http.MethodGet, Path: "/Items", ExpectedStatusCodes: []int{http.StatusOK}}
	for name, statuses := range map[string][]int{
		"a bad gateway":        {http.StatusBadGateway, http.StatusOK},
		"a busy server":        {http.StatusServiceUnavailable, http.StatusGatewayTimeout, http.StatusOK},
		"a dropped connection": {0, http.StatusOK},
		"an answer cut short":  {cutShort, http.StatusOK},
		"three in a row":       {http.StatusBadGateway, 0, cutShort, http.StatusOK},
	} {
		c, s := retrying(t, statuses...)
		resp, err := execute(t, c, get, nil)
		if err != nil || resp.StatusCode != http.StatusOK || s.requests() != len(statuses) {
			t.Errorf("%s: %v after %d requests, want the read answered on request %d", name, err, s.requests(), len(statuses))
		}
	}

	// every try failing ends in the last answer, saying how many there were
	c, s := retrying(t, http.StatusBadGateway)
	_, err := execute(t, c, get, nil)
	if StatusCode(err) != http.StatusBadGateway || s.requests() != 4 || !strings.Contains(err.Error(), "(tried 4 times)") {
		t.Errorf("four gateway answers = %v after %d requests, want the 502, tried 4 times", err, s.requests())
	}
	for _, cut := range []int{0, cutShort} {
		c, dropped := retrying(t, cut)
		if _, err := execute(t, c, get, nil); err == nil || dropped.requests() != 4 || !strings.Contains(err.Error(), "(tried 4 times)") {
			t.Errorf("four dropped connections (%d) = %v after %d requests, want an error saying it tried 4 times", cut, err, dropped.requests())
		}
	}

	// an answer that is no transient failure is not asked again
	for _, status := range []int{http.StatusInternalServerError, http.StatusNotFound, http.StatusUnauthorized} {
		c, other := retrying(t, status, http.StatusOK)
		_, err := execute(t, c, get, nil)
		if StatusCode(err) != status || other.requests() != 1 || strings.Contains(err.Error(), "tried") {
			t.Errorf("a %d = %v after %d requests, want it answered once", status, err, other.requests())
		}
	}

	// and a client with no retry sends every read once
	s = &counted{statuses: []int{http.StatusBadGateway, http.StatusOK}}
	if _, err := execute(t, serve(t, EmbyToken(testToken), s.handler), get, nil); StatusCode(err) != http.StatusBadGateway || s.requests() != 1 {
		t.Errorf("no retry = %v after %d requests, want the 502 once", err, s.requests())
	}
}

// A write or a delete is never sent again: whether the first one landed
// before the failure cannot be known from here, and a delete or an add sent
// twice is not the same as sent once.
func TestWritesAreNeverRetried(t *testing.T) {
	t.Parallel()

	for _, method := range []string{http.MethodPost, http.MethodDelete, http.MethodPut, http.MethodPatch} {
		for _, status := range []int{http.StatusBadGateway, 0, cutShort} {
			c, s := retrying(t, status, http.StatusNoContent)
			// with no body, as a delete or a mark-played goes: one with a body
			// could not be sent again as it was anyway
			_, err := execute(t, c, RequestOptions{HTTPMethod: method, Path: "/Items/1", ExpectedStatusCodes: []int{http.StatusNoContent}}, nil)
			if err == nil || s.requests() != 1 {
				t.Errorf("%s answered %d = %v after %d requests, want it sent once and failed", method, status, err, s.requests())
			}
		}
	}
}

// A retry waits as long as the policy says, and stops waiting when the
// caller gives up.
func TestRetryWaitsAndStopsWithTheContext(t *testing.T) {
	t.Parallel()

	s := &counted{statuses: []int{http.StatusServiceUnavailable}}
	c := serve(t, EmbyToken(testToken), s.handler)
	var waits []int
	var mu sync.Mutex
	c.Retry = Retry{Tries: 3, Wait: func(n int) time.Duration {
		mu.Lock()
		waits = append(waits, n)
		mu.Unlock()
		return time.Millisecond
	}}
	if _, err := execute(t, c, RequestOptions{HTTPMethod: http.MethodGet, Path: "/Items", ExpectedStatusCodes: []int{http.StatusOK}}, nil); StatusCode(err) != http.StatusServiceUnavailable {
		t.Fatalf("err = %v", err)
	}
	mu.Lock()
	if len(waits) != 2 || waits[0] != 1 || waits[1] != 2 {
		t.Errorf("waited before retries %v, want before the first and the second", waits)
	}
	mu.Unlock()
	if got := []time.Duration{Backoff(2 * time.Second)(1), Backoff(2 * time.Second)(2), Backoff(2 * time.Second)(3)}; got[0] != 2*time.Second || got[1] != 4*time.Second || got[2] != 8*time.Second {
		t.Errorf("backoff = %v, want 2s, 4s, 8s", got)
	}

	// a caller that gives up while a retry waits is not kept waiting
	s = &counted{statuses: []int{http.StatusBadGateway}}
	c = serve(t, EmbyToken(testToken), s.handler)
	c.Retry = Retry{Tries: 4, Wait: func(int) time.Duration { return time.Hour }}
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	req, err := c.NewRequest(ctx, RequestOptions{HTTPMethod: http.MethodGet, Path: "/Items", ExpectedStatusCodes: []int{http.StatusOK}})
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	if _, err := req.Execute(ctx); !errors.Is(err, context.DeadlineExceeded) || time.Since(started) > 10*time.Second || s.requests() != 1 {
		t.Errorf("a cancelled wait = %v after %v and %d requests", err, time.Since(started), s.requests())
	}
}

// A read over HTTP/2, which a reverse proxy in front of the server on TLS
// speaks, is sent again when the server or proxy gives up on it part way, as
// one over HTTP/1.1 is: there the stream is reset rather than the connection
// dropped, and that failed the read at once. A write cut short the same way
// is still sent once.
func TestReadsOverHTTP2AreRetriedAfterAStreamReset(t *testing.T) {
	t.Parallel()

	for _, h2 := range []bool{false, true} {
		for _, how := range []string{"abort", "close the connection"} {
			for _, method := range []string{http.MethodGet, http.MethodDelete} {
				var mu sync.Mutex
				seen := 0
				srv := httptest.NewUnstartedServer(nil)
				srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					mu.Lock()
					seen++
					n := seen
					mu.Unlock()
					if n > 1 {
						if _, err := io.WriteString(w, `{"Items":[]}`); err != nil {
							panic(err)
						}
						return
					}
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusOK)
					if _, err := io.WriteString(w, `{"Items":[`+strings.Repeat(`{"Id":"1"},`, 2000)); err != nil {
						panic(err)
					}
					flusher, ok := w.(http.Flusher)
					if !ok {
						panic("the test server cannot send part of an answer")
					}
					flusher.Flush()
					if how == "abort" {
						panic(http.ErrAbortHandler)
					}
					go srv.CloseClientConnections()
					<-r.Context().Done()
				})
				srv.EnableHTTP2 = h2
				srv.StartTLS()
				t.Cleanup(srv.Close)
				c, err := New(srv.URL, EmbyToken(testToken))
				if err != nil {
					t.Fatal(err)
				}
				c.HTTPClient = srv.Client()
				c.Retry = Retry{Tries: 4, Wait: func(int) time.Duration { return time.Millisecond }}

				resp, err := execute(t, c, RequestOptions{HTTPMethod: method, Path: "/Items", ExpectedStatusCodes: []int{http.StatusOK}}, nil)
				mu.Lock()
				requests := seen
				mu.Unlock()
				name := fmt.Sprintf("HTTP/2 %v, %s, %s", h2, how, method)
				if method != http.MethodGet {
					if err == nil || requests != 1 {
						t.Errorf("%s: %v after %d requests, want it sent once and failed", name, err, requests)
					}
					continue
				}
				if err != nil || requests != 2 {
					t.Errorf("%s: %v after %d requests, want the read answered on the second", name, err, requests)
					continue
				}
				if want := map[bool]int{false: 1, true: 2}[h2]; resp.ProtoMajor != want {
					t.Errorf("%s: answered over %s, want HTTP/%d", name, resp.Proto, want)
				}
			}
		}
	}
}

// Of the ways an HTTP/2 stream ends early, a read is sent again only for
// those a server or proxy uses to give up on a stream or turn it away, or
// for the connection lost under it; a stream reset for a broken protocol
// says the same request would break it again.
func TestWhichHTTP2FailuresAreTransient(t *testing.T) {
	t.Parallel()

	for msg, want := range map[string]bool{
		"stream error: stream ID 1; INTERNAL_ERROR; received from peer":                                   true,
		"stream error: stream ID 3; CANCEL; received from peer":                                           true,
		"stream error: stream ID 5; REFUSED_STREAM":                                                       true,
		"http2: client connection lost":                                                                   true,
		`http2: server sent GOAWAY and closed the connection; LastStreamID=1, ErrCode=NO_ERROR, debug=""`: true,
		"stream error: stream ID 1; PROTOCOL_ERROR; received from peer":                                   false,
		"stream error: stream ID 1; FLOW_CONTROL_ERROR":                                                   false,
		"stream error: stream ID 1; ENHANCE_YOUR_CALM":                                                    false,
	} {
		err := fmt.Errorf("GET /Items: HTTP 200, reading the answer: %w", errors.New(msg))
		if got := transient(http.StatusOK, err); got != want {
			t.Errorf("transient(%q) = %v, want %v", msg, got, want)
		}
	}
}
