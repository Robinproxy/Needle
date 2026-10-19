// Package billing defines the shared Agent/Server billing configuration contract.
package billing

import (
	"fmt"
	"regexp"
	"strings"
)

// Cost stores an exact decimal amount for the entire configured billing period.
// Empty Amount means not configured; "0" means free. A nil Cost in a report
// means an older/unconfigured Agent did not send billing settings at all.
type Cost struct {
	Currency string `json:"currency"`
	Amount   string `json:"amount"`
}

var (
	currencyPattern = regexp.MustCompile(`^[A-Z]{3}$`)
	amountPattern   = regexp.MustCompile(`^[0-9]{1,12}(\.[0-9]{1,6})?$`)
)

func (c Cost) Normalize() (Cost, error) {
	c.Currency = strings.ToUpper(strings.TrimSpace(c.Currency))
	if !currencyPattern.MatchString(c.Currency) {
		return Cost{}, fmt.Errorf("billing_currency must be a three-letter currency code (e.g. USD, CNY, EUR, GBP, CAD)")
	}
	c.Amount = strings.TrimSpace(c.Amount)
	if c.Amount == "" {
		return c, nil
	}
	if !amountPattern.MatchString(c.Amount) {
		return Cost{}, fmt.Errorf("billing_amount must be a non-negative decimal with up to 12 integer and 6 fractional digits; 0 = free")
	}
	parts := strings.SplitN(c.Amount, ".", 2)
	c.Amount = strings.TrimLeft(parts[0], "0")
	if c.Amount == "" {
		c.Amount = "0"
	}
	if len(parts) == 2 {
		if fraction := strings.TrimRight(parts[1], "0"); fraction != "" {
			c.Amount += "." + fraction
		}
	}
	return c, nil
}
