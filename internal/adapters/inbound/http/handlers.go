package http

import (
	"fmt"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/IQVO/customer-payments/internal/application/ports"
	"github.com/IQVO/customer-payments/internal/application/usecases"
)

// maxIDLength bounds a path identifier; a longer one cannot name a resource.
const maxIDLength = 128

// pathID returns the named path parameter, or false after writing 404 for one
// that can never name a resource.
func pathID(w http.ResponseWriter, r *http.Request, name string) (string, bool) {
	id := chi.URLParam(r, name)
	if id == "" || len(id) > maxIDLength {
		writeError(w, r, ports.ErrNotFound)
		return "", false
	}
	return id, true
}

func handleHealthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, statusBody{Status: "ok"})
}

// handleAuthorize answers 201 with the authorization this call created (also a
// DECLINED one) and 200 with the existing one when the checkout session
// already holds a live authorization.
func (s *Server) handleAuthorize(w http.ResponseWriter, r *http.Request) {
	var req authorizeRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	res, err := s.AuthorizePayment.Execute(r.Context(), usecases.AuthorizePaymentInput{
		CheckoutSessionID: req.CheckoutSessionID, PaymentMethodToken: req.PaymentMethodToken, Currency: req.Currency,
		TotalMinor: req.TotalMinor, Allocations: req.allocations(),
	})
	if err != nil {
		writeError(w, r, err)
		return
	}
	status := http.StatusOK
	if res.Created {
		status = http.StatusCreated
	}
	writeJSON(w, status, authorizationFrom(res.Authorization))
}

func (s *Server) handleGetAuthorization(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "authorizationId")
	if !ok {
		return
	}
	v, err := s.GetAuthorization.Execute(r.Context(), id)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, authorizationFrom(v))
}

func (s *Server) handleListAuthorizations(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	list, err := s.ListAuthorizations.Execute(r.Context(), q.Get("orderId"), q.Get("checkoutSessionId"))
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, authorizationListFrom(list))
}

func (s *Server) handleVoid(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "authorizationId")
	if !ok {
		return
	}
	v, err := s.VoidAuthorization.Execute(r.Context(), id)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, authorizationFrom(v))
}

func (s *Server) handleRequestRefund(w http.ResponseWriter, r *http.Request) {
	var req refundRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	v, err := s.RequestRefund.Execute(r.Context(), usecases.RequestRefundInput(req))
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, refundFrom(v))
}

func (s *Server) handleListRefunds(w http.ResponseWriter, r *http.Request) {
	list, err := s.ListRefunds.Execute(r.Context(), r.URL.Query().Get("orderId"))
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, refundListFrom(list))
}

func (s *Server) handleGetRefund(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "refundId")
	if !ok {
		return
	}
	v, err := s.GetRefund.Execute(r.Context(), id)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, refundFrom(v))
}

func (s *Server) handleRetryRefund(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "refundId")
	if !ok {
		return
	}
	v, err := s.RetryRefund.Execute(r.Context(), id)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, refundFrom(v))
}

// handleMetrics writes the Prometheus text exposition of the parked-facts
// gauge (H10).
func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	if s.ParkedFacts == nil {
		return
	}
	n, err := s.ParkedFacts(r.Context())
	if err != nil {
		writeError(w, r, err)
		return
	}
	_, _ = fmt.Fprintf(w, "# HELP customer_payments_parked_facts Fulfilment facts parked until their order is bound.\n"+
		"# TYPE customer_payments_parked_facts gauge\ncustomer_payments_parked_facts %d\n", n)
}
