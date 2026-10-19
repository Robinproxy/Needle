package billing

import "testing"

func TestCostExactDecimalAndCurrency(t *testing.T) {
	for _, tc := range []struct{ currency, amount, want string }{
		{"usd", "0005.500000", "5.5"}, {"CNY", "80.00", "80"}, {"EUR", "0.10", "0.1"},
		{"GBP", "0", "0"}, {"CAD", "00.000", "0"}, {"JPY", "750", "750"},
		{"CHF", "999999999999.999999", "999999999999.999999"}, {"AUD", "0.000001", "0.000001"}, {"HKD", "", ""},
	} {
		got, err := (Cost{Currency: tc.currency, Amount: tc.amount}).Normalize()
		if err != nil || got.Amount != tc.want {
			t.Fatalf("%+v: %+v, %v", tc, got, err)
		}
	}
	for _, c := range []Cost{
		{Currency: "$", Amount: "5"}, {Currency: "US", Amount: "5"}, {Currency: "USDD", Amount: "5"},
		{Currency: "USD", Amount: "-1"}, {Currency: "USD", Amount: "NaN"}, {Currency: "USD", Amount: "1e3"},
		{Currency: "USD", Amount: "1.0000001"}, {Currency: "USD", Amount: "1000000000000"},
		{Currency: "USD", Amount: "1; echo bad"}, {Currency: "USD", Amount: ".5"},
	} {
		if _, err := c.Normalize(); err == nil {
			t.Fatalf("accepted invalid cost: %+v", c)
		}
	}
}
