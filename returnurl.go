package payment

import "net/url"

// ReturnURL adds the reference as the ref query parameter so the browser
// returns to its own payment, not whichever one another tab started last.
// The reference is not an authentication token.
func ReturnURL(base, reference string) string {
	if base == "" {
		return ""
	}
	u, err := url.Parse(base)
	if err != nil {
		return base
	}
	q := u.Query()
	q.Set("ref", reference)
	u.RawQuery = q.Encode()
	return u.String()
}
