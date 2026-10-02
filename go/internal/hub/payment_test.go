package hub

import (
	"context"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/neural-hive/hive-node/internal/config"
)

func payTestHub(required bool, token string) *Hub {
	reg := NewRegistry("")
	cfg := config.Hub{
		HIVE:             config.HIVEPolicy{Default: 1, Complex: 2, Max: 10},
		PaymentRequired:  required,
		PricePerAgent:    "1",
		PricePerAgentWei: new(big.Int).Exp(big.NewInt(10), big.NewInt(18), nil),
		Treasury:         "0x7a0f3aF9cBf90B36Cf9464462976d1e82669B2c0",
		TokenAddress:     "0xC7FB869B2cD5A4fBCa1d3D145173f66D6440A817",
		SelfTestToken:    token,
		QuoteTTL:         time.Minute,
	}
	return NewWithProvider(cfg, reg, fakeProvider{}, time.Second)
}

func TestFormatHive(t *testing.T) {
	unit := new(big.Int).Exp(big.NewInt(10), big.NewInt(18), nil)
	cases := map[string]*big.Int{
		"0":    big.NewInt(0),
		"1":    unit,
		"2.5":  new(big.Int).Mul(big.NewInt(25), new(big.Int).Div(unit, big.NewInt(10))),
		"0.01": new(big.Int).Div(unit, big.NewInt(100)),
	}
	for want, wei := range cases {
		if got := FormatHive(wei); got != want {
			t.Fatalf("FormatHive(%s) = %s, want %s", wei, got, want)
		}
	}
}

func TestCostIsAgentsTimesPrice(t *testing.T) {
	h := payTestHub(true, "")
	if got := FormatHive(h.Cost(3)); got != "3" {
		t.Fatalf("cost of 3 agents = %s, want 3", got)
	}
}

func TestQuotePricesSimpleTaskAtOneAgent(t *testing.T) {
	h := payTestHub(true, "")
	q := h.Quote("What is distributed consensus?", 0, AlgConfig{})
	if q.HIVE != 1 || FormatHive(q.CostWei) != "1" {
		t.Fatalf("quote = %d agents, %s HIVE; want 1 agent, 1 HIVE", q.HIVE, FormatHive(q.CostWei))
	}
	if q.ID == "" {
		t.Fatal("quote has no id")
	}
}

func TestSettleRejectsMissingPayment(t *testing.T) {
	h := payTestHub(true, "")
	_, _, err := h.Settle(context.Background(), "", "", "x")
	pe, ok := err.(*PaymentError)
	if !ok || pe.Status != http.StatusPaymentRequired {
		t.Fatalf("want a 402 PaymentError, got %v", err)
	}
	q := h.Quote("What is distributed consensus?", 0, AlgConfig{})
	_, _, err = h.Settle(context.Background(), q.ID, "0x1234", q.Task)
	pe, ok = err.(*PaymentError)
	if !ok || pe.Status != http.StatusBadRequest {
		t.Fatalf("want a 400 for a malformed tx hash, got %v", err)
	}
	_, _, err = h.Settle(context.Background(), "q-unknown", "0x"+strings.Repeat("ab", 32), "x")
	pe, ok = err.(*PaymentError)
	if !ok || pe.Status != http.StatusPaymentRequired {
		t.Fatalf("want a 402 for an unknown quote, got %v", err)
	}
}

func TestTaskEndpointRequiresPayment(t *testing.T) {
	h := payTestHub(true, "secret")
	srv := httptest.NewServer(h.Handler(""))
	defer srv.Close()
	resp, err := http.Post(srv.URL+"/task", "application/json", strings.NewReader(`{"task":"What is distributed consensus?"}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusPaymentRequired {
		t.Fatalf("unpaid /task = %d, want 402", resp.StatusCode)
	}
}

func TestWrongSelfTestTokenIsRefused(t *testing.T) {
	h := payTestHub(true, "secret")
	srv := httptest.NewServer(h.Handler(""))
	defer srv.Close()
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/task", strings.NewReader(`{"task":"What is distributed consensus?"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Hive-Selftest", "wrong")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusPaymentRequired {
		t.Fatalf("wrong self-test token = %d, want 402", resp.StatusCode)
	}
}

func TestQuoteEndpointReturnsCost(t *testing.T) {
	h := payTestHub(true, "")
	srv := httptest.NewServer(h.Handler(""))
	defer srv.Close()
	resp, err := http.Post(srv.URL+"/quote", "application/json", strings.NewReader(`{"task":"What is distributed consensus?"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("/quote = %d, want 200", resp.StatusCode)
	}
}
