package payment_test

import (
	"net/url"
	"slices"
	"strings"
	"testing"

	"github.com/17xande-dev/payment"
	"github.com/17xande-dev/payment/payfast"
	"github.com/17xande-dev/payment/snapscan"
)

func TestRegistry_CSPAllowsLivePayFastHandover(t *testing.T) {
	pf, err := payfast.New(payfast.Config{
		MerchantID: "test-id", MerchantKey: "test-key",
		ReturnURL: "https://merchant.example/return",
		CancelURL: "https://merchant.example/cancel",
		NotifyURL: "https://merchant.example/notify",
	})
	if err != nil {
		t.Fatal(err)
	}
	snap, err := snapscan.New(snapscan.Config{
		SnapCode: "test-code", APIKey: "test-key", WebhookAuthKey: "test-webhook-key",
		SuccessURL: "https://merchant.example/return",
		FailURL:    "https://merchant.example/cancel",
	})
	if err != nil {
		t.Fatal(err)
	}
	r, err := payment.NewRegistry(pf, snap)
	if err != nil {
		t.Fatal(err)
	}
	forms, images := r.CSP()
	if !slices.Equal(images, []string{"https://pos.snapscan.io"}) {
		t.Errorf("img-src = %q, want only SnapScan's QR origin", images)
	}

	h, err := pf.Handover(payment.Request{Reference: "test-payment", Currency: "ZAR", AmountCents: 500})
	if err != nil {
		t.Fatal(err)
	}
	if h.Action != "https://www.payfast.co.za/eng/process" {
		t.Fatalf("handover POST target = %q", h.Action)
	}
	allows := httpsHostSourceMatcher(t, forms)
	for _, tc := range []struct {
		target string
		want   bool
	}{
		{h.Action, true},
		{"https://payment.payfast.io/eng/process/payment/example-id", true},
		{"http://payment.payfast.io/eng/process/payment/example-id", false},
		{"https://payment.payfast.io:8443/eng/process/payment/example-id", false},
		{"https://payment.payfast.io.example/eng/process/payment/example-id", false},
		{"https://other.payfast.io/eng/process/payment/example-id", false},
		{"https://www.payfast.co.za.example/eng/process", false},
		{"https://pos.snapscan.io/qr", false},
	} {
		if got := allows(tc.target); got != tc.want {
			t.Errorf("form-action %q allows %s = %t, want %t", forms, tc.target, got, tc.want)
		}
	}
}

// httpsHostSourceMatcher models the subset this API emits: explicit HTTPS
// host sources with no path or port. They match URLs on the default HTTPS port
// without restricting the path, including after a redirect.
func httpsHostSourceMatcher(t *testing.T, sources []string) func(string) bool {
	t.Helper()
	hosts := make(map[string]bool, len(sources))
	for _, source := range sources {
		u, err := url.Parse(source)
		if err != nil || u.Scheme != "https" || u.Host == "" ||
			u.Host != u.Hostname() || strings.Contains(u.Host, "*") ||
			source != "https://"+u.Host {
			t.Fatalf("not an explicit HTTPS host source: %q", source)
		}
		hosts[strings.ToLower(u.Host)] = true
	}
	return func(target string) bool {
		u, err := url.Parse(target)
		if err != nil {
			t.Fatalf("parse navigation URL %q: %v", target, err)
		}
		return u.Scheme == "https" && (u.Port() == "" || u.Port() == "443") &&
			hosts[strings.ToLower(u.Hostname())]
	}
}
