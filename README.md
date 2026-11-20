# payment

PayFast and SnapScan for Go applications, behind one `payment.Gateway`
interface. The module uses only the standard library. The caller owns payment
records and decides what to do once a payment is confirmed.

## v0.2.1

Live PayFast's CSP declaration now includes the hosted checkout redirect.

### Compatibility notes

`CSPOrigins` now has a `FormActions` slice. It can no longer be compared with
`==` or used as a map key. Use keyed literals rather than positional literals.
If you call `Gateway.CSP()` directly, read `FormActions` as well as `FormAction`
when building `form-action`. Callers using `Registry.CSP()` need no code change.

```go
import (
    "os"

    "github.com/17xande-dev/payment"
    "github.com/17xande-dev/payment/payfast"
    "github.com/17xande-dev/payment/snapscan"
)
```

## PayFast

```go
gateway, err := payfast.New(payfast.Config{
    MerchantID:  os.Getenv("PAYFAST_MERCHANT_ID"),
    MerchantKey: os.Getenv("PAYFAST_MERCHANT_KEY"),
    Passphrase:  os.Getenv("PAYFAST_PASSPHRASE"),
    Sandbox:     true,
    ReturnURL:   "https://merchant.example/payment/return",
    CancelURL:   "https://merchant.example/payment/cancel",
    NotifyURL:   "https://merchant.example/payments/payfast/callback",
})
```

Check the constructor error before exposing checkout. Render `Handover.Fields`
as hidden fields **in the order returned** and POST them to `Handover.Action`.
Reordering fields changes the signature. Use matching sandbox credentials with
`Sandbox: true`; a live integration also needs the matching live passphrase.

Live PayFast responds to the signed POST at `https://www.payfast.co.za/eng/process`
with a `302` to `https://payment.payfast.io/eng/process/payment/<uuid>`. Chromium
enforces `form-action` on that redirect too, so the handover page must allow both
origins. The module lists the exact checkout host, not a wildcard for unrelated
subdomains. Sandbox currently needs only `https://sandbox.payfast.co.za`.

PayFast specifies the transaction field order: `m_payment_id`, `amount`,
`item_name`, `item_description`, `custom_int1` through `custom_int5`, then
`custom_str1` through `custom_str5`. The handover signs and submits the fields
in that order, omitting blank optional values and putting `signature` last.

The `payfast` gateway normalises all form values' line breaks to CRLF before
trimming, truncating, or signing. This includes `item_name` and `name_first`:
the signature covers the same line endings the browser submits.

PayFast ITN verification checks the signature, the source-IP allowlist,
PayFast's server-to-server confirmation, and the merchant ID. Pass a bare IP
address in `payment.Notification.SourceIP`. The caller, not the library, decides
which proxies and forwarding headers it trusts. Never take a forwarding header
at face value. Do not use `AllowAnySourceIP` in production.

## SnapScan

```go
gateway, err := snapscan.New(snapscan.Config{
    SnapCode:       os.Getenv("SNAPSCAN_SNAP_CODE"),
    APIKey:         os.Getenv("SNAPSCAN_API_KEY"),
    WebhookAuthKey: os.Getenv("SNAPSCAN_WEBHOOK_AUTH_KEY"),
    ValidationKey:  os.Getenv("SNAPSCAN_VALIDATION_KEY"), // optional Secure QR key
    SuccessURL:     "https://merchant.example/payment/return",
    FailURL:        "https://merchant.example/payment/cancel",
})
```

SnapScan has no sandbox. Register the webhook endpoint with SnapScan. Render
`Handover.Action` as a link and `Handover.QRImageURL` as the QR image so the payer
can pay from a phone or scan from another device. The gateway verifies the
webhook HMAC, then confirms the payment against the merchant API. The API
response supplies the status and requested amount; a tip does not change that
requested amount.

## Starting and confirming a payment

```go
handover, err := gateway.Handover(payment.Request{
    Reference:   reference, // stable ID from the caller's payment records
    AmountCents: 29900,
    Currency:    "ZAR",
    ItemName:    "Example purchase",
})
```

Amounts are integer cents, not floats. `gateway.CSP()` gives the exact origins
the browser needs: `CSPOrigins.FormAction` is the initial form POST origin,
`CSPOrigins.FormActions` lists any additional origins (such as the live PayFast
redirect), and `CSPOrigins.ImgSrc` is the image origin. If you build a policy
directly from `gateway.CSP()`, allow both form fields, omitting empty values.
`Registry.CSP()` already combines and de-duplicates them in its first result,
even for a registry with one gateway.

`payment.Request` also accepts `ItemDescription string` for a longer purchase
description and `CustomInt [5]string` and `CustomStr [5]string` for pass-through
values. For example, `CustomStr: [5]string{"web"}` sets PayFast's `custom_str1`.
The `payfast` gateway trims surrounding whitespace, omits blank values, and
truncates `ItemDescription` and `CustomStr` values to at most 255 Unicode code
points. A non-blank `CustomInt` entry must match `-?[0-9]+` and is rejected if it
exceeds 255 characters (including the minus sign). `"0"` is sent, but `""` is
omitted. SnapScan and other gateways with nowhere to put these fields ignore them.

**A browser return proves nothing.** `ReturnURL` adds `?ref=<reference>` so the
page can look up the right payment even if several tabs are open. It is neither
authentication nor proof of payment. Only a verified server-to-server ITN or
webhook can confirm a payment.

In the callback handler, bound the body read with the provider's `MaxBodyBytes`
and pass the **unchanged** bytes, HTTP headers, and caller-resolved source IP:

```go
callback, err := gateway.ParseCallback(r.Context(), payment.Notification{
    Body: body, Header: r.Header, SourceIP: sourceIP,
})
```

`errors.Is(err, payment.ErrRetryable)` means verification could not finish, not
that it failed permanently; let the provider retry. On success, look up
`callback.Reference` and compare the expected gateway and cents before using
`callback.Paid()` to settle it. Make that update idempotent: providers can
deliver the same notification again. `callback.Ref` is the provider's
transaction ID, not the caller's reference.

## Adding a gateway

Implement `payment.Gateway` in a subpackage. Give it a stable name, validate
configuration at startup, and keep its wire format and authentication inside
the implementation. Report the hand-over shape and CSP origins the browser
needs, including any additional form redirect origins in `CSPOrigins.FormActions`.
Use scheme+host only, with no path, wildcard or semicolon. Map provider-specific
statuses to `payment.Outcome`, leaving unknown statuses pending, and
distinguish temporarily unavailable verification from
rejection. Pin signature inputs and callback checks with fixed fixtures or local
HTTP test servers.

Use `payment.NewFake` or `payment.NewFakeNamed` for handler tests, with
`payment.FakeCallbackBody` to simulate a notification. The fake does not
authenticate callbacks and must not be used in production.
