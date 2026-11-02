package http

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"time"

	"github.com/IQVO/customer-payments/internal/application/idempotency"
	"github.com/IQVO/customer-payments/internal/application/ports"
)

// IdempotencyKeyHeader is the request header a caller must supply on a route
// wrapped by RequireIdempotencyKey.
const IdempotencyKeyHeader = "Idempotency-Key"

// ReplayedHeader marks a response that was served from the stored record. It is
// set on every replay and only on a replay.
const ReplayedHeader = "Idempotency-Replayed"

// maxKeyLength bounds an Idempotency-Key.
const maxKeyLength = 128

// RequireIdempotencyKey is the Idempotency-Key middleware of every POST route
// (REST guideline 2); PUT and DELETE are idempotent by method and are not
// wrapped. The key is 1 to 128 printable ASCII characters: absent is 400
// idempotency-key-required, malformed is 400 idempotency-key-invalid.
//
// The request fingerprint is the method, the path and the SHA-256 of the body.
// The store runs the handler inside the transaction that claims the key, so the
// key row, the aggregate change and the outbox rows commit together and
// concurrent requests with one key are serialised by the unique index: the
// loser replays the winner's answer. The first answer below 500 (a 4xx too) is
// stored for 24 hours (idempotency.TTL); the same key and fingerprint replays it
// with Idempotency-Replayed: true, the same key with another fingerprint is 409
// idempotency-key-reused. A 5xx or a panic rolls back and is never stored, so
// the key can retry the real work.
func RequireIdempotencyKey(store ports.IdempotencyStore, clock ports.Clock) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			key, ok := requestKey(w, r)
			if !ok {
				return
			}
			body, ok := requestBody(w, r)
			if !ok {
				return
			}
			hash := requestHash(r, body)
			var fresh *capture
			rec, replayed, err := store.Do(r.Context(), key, hash, clock.Now().UTC().Truncate(time.Microsecond),
				func(ctx context.Context) (idempotency.Record, error) {
					fresh = &capture{header: http.Header{}}
					next.ServeHTTP(fresh, r.WithContext(ctx))
					return fresh.record(), nil
				})
			switch {
			case err != nil:
				writeError(w, r, err)
			case replayed:
				replay(w, r, rec, hash)
			default:
				fresh.flushTo(w)
			}
		})
	}
}

// requestKey returns the Idempotency-Key, or writes the 400 problem.
func requestKey(w http.ResponseWriter, r *http.Request) (string, bool) {
	key := r.Header.Get(IdempotencyKeyHeader)
	switch {
	case key == "":
		writeProblem(w, r, idempotencyKeyRequired, "send an Idempotency-Key header of 1 to 128 printable ASCII characters, unique per logical request")
	case len(key) > maxKeyLength || !printableASCII(key):
		writeProblem(w, r, idempotencyKeyInvalid, "the Idempotency-Key must be 1 to 128 printable ASCII characters")
	default:
		return key, true
	}
	return "", false
}

func printableASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < 0x20 || s[i] > 0x7e {
			return false
		}
	}
	return true
}

// requestBody reads the whole body (bounded) and restores it for the handler.
func requestBody(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxBodyBytes+1))
	_ = r.Body.Close()
	if err != nil || len(body) > maxBodyBytes {
		writeError(w, r, errMalformedRequest)
		return nil, false
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	return body, true
}

// requestHash is the request fingerprint: method + path + SHA-256 of the body.
func requestHash(r *http.Request, body []byte) string {
	sum := sha256.Sum256(body)
	h := sha256.New()
	h.Write([]byte(r.Method + " " + r.URL.EscapedPath() + "\n" + hex.EncodeToString(sum[:])))
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

// record is the answer as the store keeps it.
func (c *capture) record() idempotency.Record {
	return idempotency.Record{Status: c.status(), ContentType: c.header.Get("Content-Type"), Body: c.body.Bytes()}
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
