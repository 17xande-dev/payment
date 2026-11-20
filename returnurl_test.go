package payment

import "testing"

func TestReturnURL(t *testing.T) {
	// Keep unrelated query values and the fragment. Replace any previous ref
	// so the return still points to the payment whose hand-over was built.
	const base = "https://merchant.example/return?view=short&ref=old#result"
	const want = "https://merchant.example/return?ref=A%2BB+%26&view=short#result"
	if got := ReturnURL(base, "A+B &"); got != want {
		t.Errorf("ReturnURL = %q, want %q", got, want)
	}
}
