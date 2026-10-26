package http

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/IQVO/customer-payments/internal/application/idempotency"
	"github.com/IQVO/customer-payments/internal/application/ports"
)

// IdempotencyKeyHeader is the request header a caller must supply on a route
// wrapped by RequireIdempotencyKey.
const IdempotencyKeyHeader = "Idempotency-Key"

// ReplayedHeader marks a response that was served from the stored record.
const ReplayedHeader = "Idempotent-Replayed"

// maxKeyLength bounds an Idempotency-Key.
const maxKeyLength = 128

// RequireIdempotencyKey is the Idempotency-Key middleware of the three POST
// routes, one instance per route name. The first response below 500 for a
// (route, key) is stored for 24 hours (idempotency.TTL) in the store; the same
// key and request replays it, the same key with another request is 422
// idempotency-key-reused. A 5xx is never stored, so the key can retry the real
// work. Two concurrent first requests may both run: every command is idempotent
// by itself (the order id is derived from the key, cancel is a no-op the second
// time), and the store keeps the first record.
func RequireIdempotencyKey(store ports.IdempotencyStore, clock ports.Clock, route string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			key := r.Header.Get(IdempotencyKeyHeader)
			if key == "" || len(key) > maxKeyLength {
				writeProblem(w, r, idempotencyKeyRequired, "send an Idempotency-Key header of 1 to 128 characters, unique per logical request")
				return
			}
			body, err := io.ReadAll(io.LimitReader(r.Body, maxBodyBytes+1))
			_ = r.Body.Close()
			if err != nil || len(body) > maxBodyBytes {
				writeError(w, r, errMalformedRequest)
				return
			}
			r.Body = io.NopCloser(bytes.NewReader(body))
			hash := requestHash(r, body)

			rec, err := store.Get(r.Context(), route, key)
			switch {
			case err == nil:
				replay(w, r, rec, hash)
				return
			case !errors.Is(err, ports.ErrNotFound):
				writeError(w, r, err)
				return
			}
			rec2 := &capture{header: http.Header{}}
			next.ServeHTTP(rec2, r)
			if rec2.status() < http.StatusInternalServerError {
				stored := idempotency.Record{
					RequestHash: hash, Status: rec2.status(), ContentType: rec2.header.Get("Content-Type"),
					Body: rec2.body.Bytes(), CreatedAt: clock.Now().UTC().Truncate(time.Microsecond),
				}
				if _, err := store.Put(r.Context(), route, key, stored); err != nil {
					writeError(w, r, err)
					return
				}
			}
			rec2.flushTo(w)
		})
	}
}

func requestHash(r *http.Request, body []byte) string {
	h := sha256.New()
	h.Write([]byte(r.Method + " " + r.URL.EscapedPath() + "?" + r.URL.RawQuery + "\n"))
	h.Write(body)
	return hex.EncodeToString(h.Sum(nil))
}

func replay(w http.ResponseWriter, r *http.Request, rec idempotency.Record, hash string) {
	if rec.RequestHash != hash {
		writeProblem(w, r, idempotencyKeyReused, "the Idempotency-Key was used with a different request")
		return
	}
	if rec.ContentType != "" {
		w.Header().Set("Content-Type", rec.ContentType)
	}
	w.Header().Set(ReplayedHeader, "true")
	w.WriteHeader(rec.Status)
	_, _ = w.Write(rec.Body)
}

// capture records the handler's response so it can be stored before anything
// reaches the client.
type capture struct {
	header http.Header
	code   int
	body   bytes.Buffer
}

func (c *capture) Header() http.Header { return c.header }

func (c *capture) WriteHeader(code int) {
	if c.code == 0 {
		c.code = code
	}
}

func (c *capture) Write(b []byte) (int, error) {
	if c.code == 0 {
		c.code = http.StatusOK
	}
	return c.body.Write(b)
}

func (c *capture) status() int {
	if c.code == 0 {
		return http.StatusOK
	}
	return c.code
}

func (c *capture) flushTo(w http.ResponseWriter) {
	for k, vs := range c.header {
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(c.status())
	_, _ = w.Write(c.body.Bytes())
}
