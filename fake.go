package payment

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"sync"
)

// ErrFakeRejected is what a Fake returns for a callback it was told to refuse,
// standing in for any of the reasons a real gateway rejects one.
var ErrFakeRejected = errors.New("payment: fake gateway rejected the callback")

// Fake is a Gateway for tests. It lets callers exercise payment state changes
// and replay handling without network calls or credentials. Never enable it in
// production: it accepts callbacks without authenticating them.
type Fake struct {
	// Reject makes ParseCallback fail, standing in for a bad signature, a
	// source IP outside the allowlist, or a failed server-to-server check.
	Reject bool

	// Kind is the hand-over shape this fake produces. The zero value is a form
	// post; a test that wants the QR path sets HandoverLink. Both shapes are
	// worth exercising from the handler, since they render different pages.
	Kind HandoverKind

	// name and label let a test build two distinct fakes, which is what a
	// multi-gateway checkout needs.
	name, label string

	mu       sync.Mutex
	requests []Request
}

// NewFake returns a Fake gateway that hands over by form post.
func NewFake() *Fake { return &Fake{name: "fake", label: "Fake gateway"} }

// NewFakeNamed returns a Fake with a given name and label, for tests that need
// more than one gateway configured at once.
func NewFakeNamed(name, label string) *Fake { return &Fake{name: name, label: label} }

func (f *Fake) Name() string { return f.name }

func (f *Fake) Label() string { return f.label }

// Currency matches the real gateways so a test config needs no adjusting.
func (f *Fake) Currency() string { return "ZAR" }

func (f *Fake) CSP() CSPOrigins {
	c := CSPOrigins{}
	switch f.Kind {
	case HandoverLink:
		c.ImgSrc = "https://gateway.example"
	default:
		c.FormAction = "https://gateway.example"
	}
	return c
}

// Handover records the request and returns a hand-over that leads nowhere. The
// recorded requests are what a test asserts the checkout handed over.
func (f *Fake) Handover(r Request) (Handover, error) {
	f.mu.Lock()
	f.requests = append(f.requests, r)
	f.mu.Unlock()

	if f.Kind == HandoverLink {
		action := "https://gateway.example/qr/fake?id=" + url.QueryEscape(r.Reference) +
			"&amount=" + strconv.FormatInt(r.AmountCents, 10)
		return Handover{
			Kind:       HandoverLink,
			Action:     action,
			QRImageURL: "https://gateway.example/qr/fake.svg?id=" + url.QueryEscape(r.Reference),
		}, nil
	}

	return Handover{
		Kind:   HandoverPostForm,
		Action: "https://gateway.example/pay",
		Fields: []Field{
			{Name: "reference", Value: r.Reference},
			{Name: "amount", Value: strconv.FormatInt(r.AmountCents, 10)},
			{Name: "signature", Value: "fake-signature"},
		},
	}, nil
}

// Requests returns every request handed to Handover, in order.
func (f *Fake) Requests() []Request {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Request(nil), f.requests...)
}

// ParseCallback reads a form-encoded body with the fields FakeCallbackBody
// writes. It authenticates nothing beyond honouring Reject — proving a callback
// genuine is the one thing a fake cannot stand in for, which is why the real
// implementations have their own validation tests.
func (f *Fake) ParseCallback(_ context.Context, n Notification) (Callback, error) {
	if f.Reject {
		return Callback{}, ErrFakeRejected
	}

	values, err := url.ParseQuery(string(n.Body))
	if err != nil {
		return Callback{}, fmt.Errorf("payment: fake: parse body: %w", err)
	}

	amount := values.Get("amount")
	cents, err := ParseAmount(amount)
	if err != nil {
		return Callback{}, fmt.Errorf("payment: fake: amount %q: %w", amount, err)
	}

	// Matched case-insensitively because the fake stands in for any gateway, and
	// real ones disagree about case: PayFast shouts COMPLETE and SnapScan
	// whispers completed. Anything unrecognised stays pending, which is the rule
	// every real gateway follows too.
	status := values.Get("status")
	outcome := OutcomePending
	switch strings.ToLower(status) {
	case "paid", "complete", "completed":
		outcome = OutcomePaid
	case "failed", "error":
		outcome = OutcomeFailed
	case "cancelled":
		outcome = OutcomeCancelled
	}

	return Callback{
		Reference:   values.Get("reference"),
		Ref:         values.Get("ref"),
		Status:      status,
		Outcome:     outcome,
		Amount:      amount,
		AmountCents: cents,
		Raw:         n.Body,
	}, nil
}

// FakeCallbackBody builds the body a Fake understands, so tests write a callback
// the same way in every one of them.
func FakeCallbackBody(reference, ref, status string, amountCents int64) []byte {
	return []byte(url.Values{
		"reference": {reference},
		"ref":       {ref},
		"status":    {status},
		"amount":    {FormatAmount(amountCents)},
	}.Encode())
}
