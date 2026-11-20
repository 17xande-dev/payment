package payfast

import (
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/17xande-dev/payment"
)

const sandboxSignatureBase = "merchant_id=10000100&merchant_key=46f0cd694581a" +
	"&return_url=https%3A%2F%2Fstore.example%2Fcart%2Fcheckout%2Fsuccess%3Fref%3D3f2504e0-4f89-41d3-9a0c-0305e82c3301" +
	"&cancel_url=https%3A%2F%2Fstore.example%2Fcart%2Fcheckout%2Fcancel%3Fref%3D3f2504e0-4f89-41d3-9a0c-0305e82c3301" +
	"&notify_url=https%3A%2F%2Fstore.example%2Fpayments%2Fpayfast%2Fcallback" +
	"&name_first=Jane&name_last=Doe&email_address=jane%40example.com" +
	"&m_payment_id=3f2504e0-4f89-41d3-9a0c-0305e82c3301" +
	"&amount=299.00&item_name=Test+Store+order+3F2504E0"

const sandboxSignaturePassphrase = "&passphrase=jt7NOE43FZPn"

func TestPayFast_HandoverOptionalSignatureVectors(t *testing.T) {
	g := testGateway(t, nil)
	const description = "&item_description=Gift+wrap+%26+card"

	// Computed independently: printf '%s' "$base$description$suffix$passphrase" | md5sum
	for _, tc := range []struct {
		name     string
		ints     [5]string
		strs     [5]string
		optional []payment.Field
		suffix   string
		digest   string
	}{
		{
			name: "description and one string",
			ints: [5]string{2: "\t "},
			strs: [5]string{" North + West ", 2: "\u2003\t"},
			optional: []payment.Field{
				{Name: "item_description", Value: "Gift wrap & card"},
				{Name: "custom_str1", Value: "North + West"},
			},
			suffix: "&custom_str1=North+%2B+West",
			digest: "d509aaa9801326364ec41e117446b0bf",
		},
		{
			name: "description and all custom fields",
			ints: [5]string{" \t-7 ", "02", "0", "-4", "5"},
			strs: [5]string{" North + West ", "second", "third", "fourth", "fifth"},
			optional: []payment.Field{
				{Name: "item_description", Value: "Gift wrap & card"},
				{Name: "custom_int1", Value: "-7"},
				{Name: "custom_int2", Value: "02"},
				{Name: "custom_int3", Value: "0"},
				{Name: "custom_int4", Value: "-4"},
				{Name: "custom_int5", Value: "5"},
				{Name: "custom_str1", Value: "North + West"},
				{Name: "custom_str2", Value: "second"},
				{Name: "custom_str3", Value: "third"},
				{Name: "custom_str4", Value: "fourth"},
				{Name: "custom_str5", Value: "fifth"},
			},
			suffix: "&custom_int1=-7&custom_int2=02&custom_int3=0&custom_int4=-4&custom_int5=5" +
				"&custom_str1=North+%2B+West&custom_str2=second&custom_str3=third&custom_str4=fourth&custom_str5=fifth",
			digest: "5362dac646a8741c3fdbba47cceed3eb",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := testRequest()
			req.ItemDescription = "\u2003Gift wrap & card\t "
			req.CustomInt, req.CustomStr = tc.ints, tc.strs
			h, err := g.Handover(req)
			if err != nil {
				t.Fatal(err)
			}
			item := payment.Field{Name: "item_name", Value: req.ItemName}
			i := slices.Index(h.Fields, item)
			if i == -1 {
				t.Fatal("missing item_name")
			}
			want := append(slices.Clone(tc.optional), payment.Field{Name: "signature", Value: tc.digest})
			if got := h.Fields[i+1:]; !slices.Equal(got, want) {
				t.Errorf("fields after item_name:\n got %v\nwant %v", got, want)
			}
			if got, want := signatureString(h.Fields[:len(h.Fields)-1], g.cfg.Passphrase), sandboxSignatureBase+description+tc.suffix+sandboxSignaturePassphrase; got != want {
				t.Errorf("signed string:\n got %s\nwant %s", got, want)
			}
		})
	}
}

func TestPayFast_HandoverBlankOptionalFieldsAreUnchanged(t *testing.T) {
	g := testGateway(t, nil)
	req := testRequest()
	want, err := g.Handover(req)
	if err != nil {
		t.Fatal(err)
	}
	req.ItemDescription = "\u2003\t"
	req.CustomInt[0] = " \n "
	req.CustomStr[4] = "\r\t"
	got, err := g.Handover(req)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("blank optional fields changed the handover:\n got %+v\nwant %+v", got, want)
	}
}

func TestPayFast_HandoverTruncatesOptionalFieldsAt255Runes(t *testing.T) {
	req := testRequest()
	description := strings.Repeat("é", 255)
	custom := strings.Repeat("界", 255)
	integer := "-" + strings.Repeat("9", 254)
	req.ItemDescription = " " + description + "🙂\t"
	req.CustomStr[4] = "\t" + custom + "🙂 "
	req.CustomInt[4] = " " + integer + " "
	h, err := testGateway(t, nil).Handover(req)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []payment.Field{
		{Name: "item_description", Value: description},
		{Name: "custom_int5", Value: integer},
		{Name: "custom_str5", Value: custom},
	} {
		if got := field(h.Fields, want.Name); got != want.Value {
			t.Errorf("%s = %q, want %q", want.Name, got, want.Value)
		}
	}
}

func TestPayFast_HandoverRejectsNonIntegerCustomValues(t *testing.T) {
	g := testGateway(t, nil)
	for _, tc := range []struct{ name, value string }{
		{"decimal", " 1.5 "},
		{"sign without digits", "-"},
		{"leading plus", "+4"},
		{"exponent", "1e3"},
		{"embedded space", "1 2"},
		{"non-ASCII digit", "９"},
		{"invalid suffix past the limit", strings.Repeat("9", 255) + "x"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := testRequest()
			req.CustomInt[3] = tc.value
			h, err := g.Handover(req)
			if err == nil || err.Error() != "payfast: custom_int4 must be a decimal integer" {
				t.Errorf("Handover error = %v, want a clear custom_int4 error", err)
			}
			if !reflect.DeepEqual(h, payment.Handover{}) {
				t.Errorf("invalid custom integer produced a handover: %+v", h)
			}
		})
	}
}

func TestPayFast_HandoverRejectsOverlongCustomInteger(t *testing.T) {
	req := testRequest()
	req.CustomInt[4] = " -" + strings.Repeat("9", 255) + " "
	h, err := testGateway(t, nil).Handover(req)
	if err == nil || err.Error() != "payfast: custom_int5 exceeds 255 characters" {
		t.Errorf("Handover error = %v, want a clear custom_int5 length error", err)
	}
	if !reflect.DeepEqual(h, payment.Handover{}) {
		t.Errorf("overlong custom integer produced a handover: %+v", h)
	}
}

func TestPayFast_HandoverSignsBrowserLineBreaks(t *testing.T) {
	g := testGateway(t, nil)
	// printf '%s' "$base&item_description=Gift+wrap%0D%0AAdd+a+card$passphrase" | md5sum
	const wantString = sandboxSignatureBase + "&item_description=Gift+wrap%0D%0AAdd+a+card" + sandboxSignaturePassphrase
	const wantDigest = "a1fa4f78e1402cf48fc4af67812fa59e"
	for _, tc := range []struct{ name, breakText string }{
		{"LF", "\n"},
		{"CRLF", "\r\n"},
		{"CR", "\r"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := testRequest()
			req.ItemDescription = "Gift wrap" + tc.breakText + "Add a card"
			h, err := g.Handover(req)
			if err != nil {
				t.Fatal(err)
			}
			if got := field(h.Fields, "item_description"); got != "Gift wrap\r\nAdd a card" {
				t.Errorf("item_description = %q, want CRLF", got)
			}
			if got := signatureString(h.Fields[:len(h.Fields)-1], g.cfg.Passphrase); got != wantString {
				t.Errorf("signed string = %q, want %q", got, wantString)
			}
			if got := field(h.Fields, "signature"); got != wantDigest {
				t.Errorf("signature = %q, want %q", got, wantDigest)
			}
		})
	}
}

func TestPayFast_HandoverNormalisesBeforeTruncatingFormValues(t *testing.T) {
	req := testRequest()
	req.NameFirst = "Jane\rMarie\nDoe\r\nLee"
	// An early LF expands, consuming one more byte/rune before the cut.
	// A second LF normalises to CRLF across that cut and must not be sent.
	itemPrefix := "A\r\n" + strings.Repeat("x", 96)
	optionalPrefix := "A\r\n" + strings.Repeat("é", 251)
	req.ItemName = "A\n" + strings.Repeat("x", 96) + "\nB"
	req.ItemDescription = "A\n" + strings.Repeat("é", 251) + "\nB"
	req.CustomStr[4] = req.ItemDescription
	h, err := testGateway(t, nil).Handover(req)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []payment.Field{
		{Name: "name_first", Value: "Jane\r\nMarie\r\nDoe\r\nLee"},
		{Name: "item_name", Value: itemPrefix},
		{Name: "item_description", Value: optionalPrefix},
		{Name: "custom_str5", Value: optionalPrefix},
	} {
		if got := field(h.Fields, want.Name); got != want.Value {
			t.Errorf("%s = %q, want %q", want.Name, got, want.Value)
		}
	}
}
