package money

import "testing"

func TestParseMajor(t *testing.T) {
	cases := []struct {
		cur  Currency
		in   string
		want Minor
		ok   bool
	}{
		{"USD", "12.34", 1234, true},
		{"USD", "0.05", 5, true},
		{"USD", "-0.05", -5, true},
		{"USD", "1000", 100000, true},
		{"USD", "+7.00", 700, true},
		{"USD", "0", 0, true},
		{"USD", ".5", 50, true},
		{"USD", "12.345", 0, false}, // too many decimals
		{"USD", "12.3.4", 0, false},
		{"USD", "abc", 0, false},
		{"USD", "", 0, false},
		{"JPY", "100", 0, false}, // unsupported currency
	}
	for _, c := range cases {
		got, err := ParseMajor(c.cur, c.in)
		if c.ok && err != nil {
			t.Errorf("ParseMajor(%s,%q) unexpected err: %v", c.cur, c.in, err)
			continue
		}
		if !c.ok && err == nil {
			t.Errorf("ParseMajor(%s,%q) expected error, got %d", c.cur, c.in, got)
			continue
		}
		if c.ok && got != c.want {
			t.Errorf("ParseMajor(%s,%q) = %d, want %d", c.cur, c.in, got, c.want)
		}
	}
}

func TestFormatMajorRoundTrip(t *testing.T) {
	for _, v := range []Minor{0, 5, -5, 1234, -1234, 100000, 999999999} {
		s, err := FormatMajor("USD", v)
		if err != nil {
			t.Fatalf("FormatMajor(%d): %v", v, err)
		}
		back, err := ParseMajor("USD", s)
		if err != nil {
			t.Fatalf("ParseMajor(%q): %v", s, err)
		}
		if back != v {
			t.Errorf("round trip %d -> %q -> %d", v, s, back)
		}
	}
}

func TestFormatMajor(t *testing.T) {
	cases := []struct {
		v    Minor
		want string
	}{
		{1234, "12.34"},
		{5, "0.05"},
		{-5, "-0.05"},
		{100000, "1000.00"},
		{0, "0.00"},
	}
	for _, c := range cases {
		got, err := FormatMajor("USD", c.v)
		if err != nil {
			t.Fatal(err)
		}
		if got != c.want {
			t.Errorf("FormatMajor(%d) = %q, want %q", c.v, got, c.want)
		}
	}
}

func TestRateConvert(t *testing.T) {
	// 1 USD = 0.92 EUR expressed as 92/100.
	r := Rate{Num: 92, Den: 100}
	cases := []struct {
		from Minor
		want Minor
	}{
		{10000, 9200}, // 100.00 USD -> 92.00 EUR
		{100, 92},     // 1.00 USD -> 0.92 EUR
		{101, 93},     // 1.01 USD -> 0.9292 -> round-half-up -> 93
		{-10000, -9200},
		{0, 0},
	}
	for _, c := range cases {
		got, err := r.Convert(c.from)
		if err != nil {
			t.Fatal(err)
		}
		if got != c.want {
			t.Errorf("Convert(%d) = %d, want %d", c.from, got, c.want)
		}
	}
}

func TestRateConvertRoundHalfUp(t *testing.T) {
	// rate 1/2: converting an odd number should round the .5 up (away from 0).
	r := Rate{Num: 1, Den: 2}
	got, err := r.Convert(3) // 3 * 1 / 2 = 1.5 -> 2
	if err != nil {
		t.Fatal(err)
	}
	if got != 2 {
		t.Errorf("Convert(3) with 1/2 = %d, want 2", got)
	}
}

func TestRateValidate(t *testing.T) {
	for _, r := range []Rate{{0, 1}, {1, 0}, {-1, 2}, {2, -1}} {
		if err := r.Validate(); err == nil {
			t.Errorf("Rate%v expected invalid", r)
		}
	}
}

func TestNoFloatOverflowSafety(t *testing.T) {
	// Large amount * large numerator would overflow int64 if done naively;
	// big.Int path must stay exact.
	r := Rate{Num: 1_000_000, Den: 1}
	got, err := r.Convert(9_000_000_000) // 9e9 * 1e6 = 9e15, fits in int64
	if err != nil {
		t.Fatal(err)
	}
	if got != 9_000_000_000_000_000 {
		t.Errorf("got %d", got)
	}
}
