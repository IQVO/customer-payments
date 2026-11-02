// Package paymentprovider is the HTTP adapter of the payment-provider port
// (the Anti-Corruption Layer). It speaks the provider API recorded in
// docs/adr/0002-customer-payments-assumptions.md (A12) against a configurable
// base URL (PAYMENT_PROVIDER_URL); the payment-provider-simulator is its
// stand-in. Every call carries the Idempotency-Key the use case derived, and
// only the PSP token ever crosses this boundary, never a card number.
package paymentprovider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/IQVO/customer-payments/internal/application/ports"
	"github.com/IQVO/customer-payments/internal/application/provider"
)

// DefaultTimeout bounds one provider call.
const DefaultTimeout = 10 * time.Second

// maxResponseBytes bounds a provider response body.
const maxResponseBytes = 1 << 20

// Client implements ports.PaymentProvider over HTTP.
type Client struct {
	base *url.URL
	http *http.Client
}

var _ ports.PaymentProvider = (*Client)(nil)

// New builds a client for the provider at baseURL; a nil httpClient gets
// DefaultTimeout.
func New(baseURL string, httpClient *http.Client) (*Client, error) {
	u, err := url.Parse(baseURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("payment provider URL %q is not an http(s) URL", baseURL)
	}
	if httpClient == nil {
		httpClient = &http.Client{Timeout: DefaultTimeout}
	}
	return &Client{base: u, http: httpClient}, nil
}

type amountBody struct {
	AmountMinor int64 `json:"amountMinor"`
}

type authorizeBody struct {
	AmountMinor        int64  `json:"amountMinor"`
	Currency           string `json:"currency"`
	PaymentMethodToken string `json:"paymentMethodToken"`
	Reference          string `json:"reference"`
}

type holdAnswer struct {
	ID        string    `json:"id"`
	ExpiresAt time.Time `json:"expiresAt"`
}

type idAnswer struct {
	ID string `json:"id"`
}

type declineAnswer struct {
	Code string `json:"code"`
}

// Authorize implements ports.PaymentProvider.
func (c *Client) Authorize(ctx context.Context, req provider.AuthorizeRequest) (provider.Hold, error) {
	var a holdAnswer
	body := authorizeBody{AmountMinor: req.AmountMinor, Currency: req.Currency, PaymentMethodToken: req.PaymentMethodToken, Reference: req.Reference}
	if err := c.post(ctx, "/authorizations", req.Key, body, &a); err != nil {
		return provider.Hold{}, err
	}
	return a.hold()
}

// Capture implements ports.PaymentProvider.
func (c *Client) Capture(ctx context.Context, key, holdID string, amountMinor int64) (string, error) {
	return c.idCall(ctx, key, holdID, "captures", amountMinor)
}

// Void implements ports.PaymentProvider.
func (c *Client) Void(ctx context.Context, key, holdID string) error {
	return c.post(ctx, "/authorizations/"+url.PathEscape(holdID)+"/voids", key, struct{}{}, nil)
}

// Refund implements ports.PaymentProvider.
func (c *Client) Refund(ctx context.Context, key, holdID string, amountMinor int64) (string, error) {
	return c.idCall(ctx, key, holdID, "refunds", amountMinor)
}

// Reauthorize implements ports.PaymentProvider.
func (c *Client) Reauthorize(ctx context.Context, key, holdID string, amountMinor int64) (provider.Hold, error) {
	var a holdAnswer
	if err := c.post(ctx, "/authorizations/"+url.PathEscape(holdID)+"/reauthorizations", key, amountBody{amountMinor}, &a); err != nil {
		return provider.Hold{}, err
	}
	return a.hold()
}

// ReduceAuthorization implements ports.PaymentProvider.
func (c *Client) ReduceAuthorization(ctx context.Context, key, holdID string, amountMinor int64) error {
	return c.post(ctx, "/authorizations/"+url.PathEscape(holdID)+"/reductions", key, amountBody{amountMinor}, nil)
}

func (a holdAnswer) hold() (provider.Hold, error) {
	if a.ID == "" || a.ExpiresAt.IsZero() {
		return provider.Hold{}, errors.New("payment provider answered a hold without id or expiry")
	}
	return provider.Hold{ID: a.ID, ExpiresAt: a.ExpiresAt.UTC()}, nil
}

func (c *Client) idCall(ctx context.Context, key, holdID, sub string, amountMinor int64) (string, error) {
	var a idAnswer
	if err := c.post(ctx, "/authorizations/"+url.PathEscape(holdID)+"/"+sub, key, amountBody{amountMinor}, &a); err != nil {
		return "", err
	}
	if a.ID == "" {
		return "", errors.New("payment provider answered without an id")
	}
	return a.ID, nil
}

// post sends one idempotent JSON POST. 2xx decodes into out (when non-nil);
// 402 is a definitive decline; a transport failure, a timeout, 429 and 5xx are
// provider.ErrUnavailable; any other status is a contract error.
func (c *Client) post(ctx context.Context, path, key string, body, out any) error {
	raw, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("encode provider request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base.JoinPath(path).String(), bytes.NewReader(raw))
	if err != nil {
		return fmt.Errorf("build provider request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", key)
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("%w: %v", provider.ErrUnavailable, err)
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return fmt.Errorf("%w: reading the answer: %v", provider.ErrUnavailable, err)
	}
	return answer(resp.StatusCode, data, out)
}

func answer(status int, data []byte, out any) error {
	switch {
	case status >= 200 && status < 300:
		if out == nil {
			return nil
		}
		if err := json.Unmarshal(data, out); err != nil {
			return fmt.Errorf("decode provider answer: %w", err)
		}
		return nil
	case status == http.StatusPaymentRequired:
		var d declineAnswer
		_ = json.Unmarshal(data, &d)
		if d.Code == "" {
			d.Code = "DECLINED"
		}
		return provider.Declined(d.Code)
	case status == http.StatusTooManyRequests || status >= 500:
		return fmt.Errorf("%w: status %d", provider.ErrUnavailable, status)
	}
	return fmt.Errorf("payment provider rejected the request: status %d: %.200s", status, data)
}
