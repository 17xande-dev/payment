package payment

import "testing"

func TestFakeReference(t *testing.T) {
	f := NewFake()
	const reference = "checkout + 123"
	h, err := f.Handover(Request{Reference: reference, AmountCents: 500})
	if err != nil {
		t.Fatal(err)
	}
	if len(h.Fields) == 0 || h.Fields[0] != (Field{Name: "reference", Value: reference}) {
		t.Fatalf("fake hand-over fields = %v, want caller reference first", h.Fields)
	}

	const body = "amount=5.00&ref=gateway-123&reference=checkout+%2B+123&status=paid"
	if got := string(FakeCallbackBody(reference, "gateway-123", "paid", 500)); got != body {
		t.Fatalf("FakeCallbackBody = %q, want %q", got, body)
	}
	cb, err := f.ParseCallback(t.Context(), Notification{Body: []byte(body)})
	if err != nil {
		t.Fatal(err)
	}
	// Keep the caller's reference distinct from the gateway's transaction ID.
	if cb.Reference != reference || cb.Ref != "gateway-123" || cb.AmountCents != 500 || !cb.Paid() {
		t.Errorf("fake callback = %+v", cb)
	}
}
