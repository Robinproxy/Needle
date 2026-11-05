package server

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
)

const ecbURL = "https://www.ecb.europa.eu/stats/eurofxref/eurofxref-daily.xml"
const displayCurrencies = " CNY USD EUR GBP CAD HKD JPY AUD "

var ratePattern = regexp.MustCompile(`^[0-9]{1,8}(\.[0-9]{1,8})?$`)
var rateCurrencyPattern = regexp.MustCompile(`^[A-Z]{3}$`)

type costDisplay struct {
	Currency string            `json:"currency"`
	Source   string            `json:"source"`
	Date     string            `json:"date"`
	Stale    bool              `json:"stale"`
	Rates    map[string]string `json:"rates"` // All quotes share one EUR base and date.
}

type exchangeStatus struct {
	costDisplay
	LastAttempt int64  `json:"last_attempt"`
	LastSuccess int64  `json:"last_success"`
	LastError   string `json:"last_error"`
}

func (s *Store) migrateExchangeRates() error {
	_, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS cost_display (
 id INTEGER PRIMARY KEY CHECK(id=1), currency TEXT NOT NULL DEFAULT 'CNY',
 rate_date TEXT NOT NULL DEFAULT '', rates_json TEXT NOT NULL DEFAULT '{}',
 last_attempt INTEGER NOT NULL DEFAULT 0, last_success INTEGER NOT NULL DEFAULT 0,
 last_error TEXT NOT NULL DEFAULT ''); INSERT OR IGNORE INTO cost_display(id) VALUES(1)`)
	return err
}

func (s *Store) exchangeStatus(now time.Time) (exchangeStatus, error) {
	status := exchangeStatus{costDisplay: costDisplay{Source: "ECB"}}
	var encoded string
	err := s.db.QueryRow(`SELECT currency,rate_date,rates_json,last_attempt,last_success,last_error FROM cost_display WHERE id=1`).Scan(
		&status.Currency, &status.Date, &encoded, &status.LastAttempt, &status.LastSuccess, &status.LastError)
	if err != nil {
		return status, err
	}
	if err := json.Unmarshal([]byte(encoded), &status.Rates); err != nil {
		return status, err
	}
	date, err := time.Parse("2006-01-02", status.Date)
	// Age the source's observation, not the last successful HTTP request.
	today := now.UTC().Truncate(24 * time.Hour)
	status.Stale = err == nil && today.Sub(date) > 7*24*time.Hour
	return status, nil
}

func (s *Store) setCostCurrency(currency string) error {
	if !strings.Contains(displayCurrencies, " "+currency+" ") || len(currency) != 3 {
		return fmt.Errorf("unsupported display currency")
	}
	_, err := s.db.Exec(`UPDATE cost_display SET currency=? WHERE id=1`, currency)
	return err
}

func parseECBRates(body []byte, now time.Time) (string, map[string]string, error) {
	var document struct {
		XMLName xml.Name `xml:"http://www.gesmes.org/xml/2002-08-01 Envelope"`
		Cube    []struct {
			Days []struct {
				Date   string `xml:"time,attr"`
				Quotes []struct {
					Currency string `xml:"currency,attr"`
					Rate     string `xml:"rate,attr"`
				} `xml:"Cube"`
			} `xml:"Cube"`
		} `xml:"Cube"`
	}
	decoder := xml.NewDecoder(strings.NewReader(string(body)))
	if err := decoder.Decode(&document); err != nil || len(document.Cube) != 1 || len(document.Cube[0].Days) != 1 {
		return "", nil, fmt.Errorf("invalid ECB document")
	}
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if text, ok := token.(xml.CharData); err != nil || !ok || strings.TrimSpace(string(text)) != "" {
			return "", nil, fmt.Errorf("unexpected trailing ECB content")
		}
	}
	day := document.Cube[0].Days[0]
	date, err := time.Parse("2006-01-02", day.Date)
	if err != nil || date.Year() < 1999 || date.After(now.UTC()) || len(day.Quotes) > 63 {
		return "", nil, fmt.Errorf("invalid ECB observation date")
	}
	rates := map[string]string{"EUR": "1"}
	for _, quote := range day.Quotes {
		if !rateCurrencyPattern.MatchString(quote.Currency) || !ratePattern.MatchString(quote.Rate) || rates[quote.Currency] != "" || strings.Trim(quote.Rate, "0.") == "" {
			return "", nil, fmt.Errorf("invalid or duplicate ECB quote")
		}
		rates[quote.Currency] = quote.Rate
	}
	for _, currency := range strings.Fields(displayCurrencies) {
		if rates[currency] == "" {
			return "", nil, fmt.Errorf("incomplete ECB quotes")
		}
	}
	return day.Date, rates, nil
}

// HTTP and validation failures only update status; never erase the last good
// observation. The fixed HTTPS URL is not editable by Agents or browser users.
func (s *Store) updateExchangeRates(ctx context.Context, client *http.Client, now time.Time) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ecbURL, nil)
	if err != nil {
		return err
	}
	date, rates := "", map[string]string(nil)
	resp, err := client.Do(req)
	if err == nil {
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			err = fmt.Errorf("ECB returned HTTP %d", resp.StatusCode)
		} else {
			var body []byte
			body, err = io.ReadAll(io.LimitReader(resp.Body, (256<<10)+1))
			if err == nil && len(body) > 256<<10 {
				err = fmt.Errorf("ECB response too large")
			}
			if err == nil {
				date, rates, err = parseECBRates(body, now)
			}
		}
	}
	if err == nil {
		encoded, _ := json.Marshal(rates)
		result, saveErr := s.db.Exec(`UPDATE cost_display SET rate_date=?,rates_json=?,last_attempt=?,last_success=?,last_error='' WHERE id=1 AND rate_date<=?`, date, string(encoded), now.Unix(), now.Unix(), date)
		err = saveErr
		if err == nil {
			n, e := result.RowsAffected()
			if e != nil {
				return e
			}
			if n == 1 {
				return nil
			}
			err = fmt.Errorf("ECB observation is older than cached rates")
		}
	}
	// Do not persist URLs or transport errors that may contain proxy credentials.
	message := "ECB update failed; retaining cached rates"
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if _, saveErr := s.db.Exec(`UPDATE cost_display SET last_attempt=?,last_error=? WHERE id=1`, now.Unix(), message); saveErr != nil {
		return saveErr
	}
	return err
}

// Only the serving process starts this worker, never CLI commands or tests.
// Persisted attempt times prevent restart loops; failures retry at most hourly.
// Return a stop function that cancels HTTP before the database is closed.
func (s *Store) StartExchangeRates() func() {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	client := &http.Client{Timeout: 12 * time.Second, CheckRedirect: func(r *http.Request, via []*http.Request) error {
		if len(via) >= 3 || r.URL.Scheme != "https" || r.URL.Host != "www.ecb.europa.eu" {
			return fmt.Errorf("ECB redirect rejected")
		}
		return nil
	}}
	go func() {
		defer close(done)
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		for {
			now := time.Now()
			status, err := s.exchangeStatus(now)
			if err == nil && exchangeUpdateDue(status, now) {
				err = s.updateExchangeRates(ctx, client, now)
			}
			if ctx.Err() != nil {
				return
			}
			s.logs.Check("exchange_rates", "WARN", err)
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	return func() { cancel(); <-done }
}

func exchangeUpdateDue(status exchangeStatus, now time.Time) bool {
	interval := 24 * time.Hour
	if status.LastError != "" {
		interval = time.Hour
	}
	return status.LastAttempt == 0 || now.Sub(time.Unix(status.LastAttempt, 0)) >= interval
}
