package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"needle/internal/billing"
)

var ErrProfileConflict = errors.New("node settings changed; reload before saving")

func (s *Store) migrateNodeProfiles() error {
	for _, definition := range []string{
		"display_name TEXT NOT NULL DEFAULT ''",
		"profile_managed INTEGER NOT NULL DEFAULT 0",
		"profile_revision INTEGER NOT NULL DEFAULT 0",
		"traffic_reset_day INTEGER",
	} {
		if _, err := s.db.Exec("ALTER TABLE agents ADD COLUMN " + definition); err != nil && !strings.Contains(err.Error(), "duplicate column name") {
			return err
		}
	}
	_, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS admin_account (
 id INTEGER PRIMARY KEY CHECK(id=1),username TEXT NOT NULL,password_hash TEXT NOT NULL,
 origin TEXT NOT NULL,generation TEXT NOT NULL);
 CREATE TABLE IF NOT EXISTS node_profile_audit (
 id INTEGER PRIMARY KEY,agent_id INTEGER NOT NULL,actor TEXT NOT NULL,
 changed_at INTEGER NOT NULL,before_json TEXT NOT NULL,after_json TEXT NOT NULL);`)
	return err
}

type NodeProfileInput struct {
	Version               int64  `json:"version"`
	OrderVersion          int64  `json:"order_version"`
	DisplayName           string `json:"display_name"`
	Region                string `json:"region"`
	BillingCurrency       string `json:"billing_currency"`
	BillingAmount         string `json:"billing_amount"`
	BillingPeriod         string `json:"billing_period"`
	ExpiresAt             string `json:"expires_at"`
	MonthlyTrafficLimitGB string `json:"monthly_traffic_limit_gb"`
	Position              int    `json:"position"`
}

type NodeProfile struct {
	NodeProfileInput
	ID              int64  `json:"id"`
	Hostname        string `json:"hostname"`
	Managed         bool   `json:"managed"`
	TrafficResetDay int    `json:"traffic_reset_day"`
	NextRenewal     string `json:"next_renewal"`
}

func nodeProfile(a AgentRow, position int, orderVersion int64, now time.Time) NodeProfile {
	p := NodeProfile{NodeProfileInput: NodeProfileInput{
		Version: a.ProfileRevision, OrderVersion: orderVersion, DisplayName: a.DisplayName,
		Region: a.Region, BillingPeriod: a.BillingPeriod, Position: position,
		MonthlyTrafficLimitGB: strconv.FormatInt(a.MonthlyTrafficLimit/1_000_000_000, 10),
	}, ID: a.ID, Hostname: a.Hostname, Managed: a.ProfileManaged}
	if remainder := a.MonthlyTrafficLimit % 1_000_000_000; remainder != 0 {
		p.MonthlyTrafficLimitGB += "." + strings.TrimRight(fmt.Sprintf("%09d", remainder), "0")
	}
	if a.BillingCost != nil {
		p.BillingCurrency, p.BillingAmount = a.BillingCost.Currency, a.BillingCost.Amount
	}
	if p.BillingCurrency == "" {
		p.BillingCurrency = "USD"
	}
	if a.ExpiresAt != nil {
		p.ExpiresAt = billingAnchor(*a.ExpiresAt, now.Location()).Format("2006-01-02")
		_, p.NextRenewal = calcRenewalDue(*a.ExpiresAt, a.BillingPeriod, now)
	}
	if a.TrafficResetDay != nil {
		p.TrafficResetDay = *a.TrafficResetDay
	} else {
		p.TrafficResetDay, _ = trafficResetDay(a.BillingPeriod, a.ExpiresAt)
	}
	return p
}

func (p *NodeProfileInput) normalize(now time.Time) (*billing.Cost, *int64, int64, error) {
	p.DisplayName = strings.TrimSpace(p.DisplayName)
	if !utf8.ValidString(p.DisplayName) || utf8.RuneCountInString(p.DisplayName) > 253 || hasControl(p.DisplayName) {
		return nil, nil, 0, fmt.Errorf("invalid display name")
	}
	p.Region = strings.ToUpper(strings.TrimSpace(p.Region))
	if p.Region != "" && !regionCodeRe.MatchString(p.Region) {
		return nil, nil, 0, fmt.Errorf("region must be an ISO two-letter code or empty")
	}
	cost, err := (billing.Cost{Currency: p.BillingCurrency, Amount: p.BillingAmount}).Normalize()
	if err != nil {
		return nil, nil, 0, err
	}
	p.BillingCurrency, p.BillingAmount = cost.Currency, cost.Amount
	switch p.BillingPeriod {
	case "":
		if cost.Amount != "" || p.ExpiresAt != "" {
			return nil, nil, 0, fmt.Errorf("select a payment period before setting cost or renewal date")
		}
	case "forever":
		if p.ExpiresAt != "" {
			return nil, nil, 0, fmt.Errorf("permanent nodes must have an empty renewal date")
		}
	case "1m", "3m", "6m", "12m", "24m", "36m":
		if p.ExpiresAt == "" {
			return nil, nil, 0, fmt.Errorf("a renewal date is required for this payment period")
		}
	default:
		return nil, nil, 0, fmt.Errorf("invalid payment period")
	}
	var expiry *int64
	if p.ExpiresAt != "" {
		date, err := time.Parse("2006-01-02", p.ExpiresAt)
		if err != nil || date.Format("2006-01-02") != p.ExpiresAt || date.Before(now.AddDate(-20, 0, 0)) || date.After(now.AddDate(20, 0, 0)) {
			return nil, nil, 0, fmt.Errorf("invalid renewal date")
		}
		value := date.Add(12 * time.Hour).Unix()
		expiry = &value
	}
	// Decimal arithmetic preserves the installer contract without float rounding.
	parts := strings.Split(p.MonthlyTrafficLimitGB, ".")
	if len(parts) > 2 || parts[0] == "" || len(parts[0]) > 7 {
		return nil, nil, 0, fmt.Errorf("invalid monthly allowance")
	}
	for _, ch := range strings.Join(parts, "") {
		if ch < '0' || ch > '9' {
			return nil, nil, 0, fmt.Errorf("monthly allowance must be a non-negative number in GB")
		}
	}
	whole, _ := strconv.ParseInt(parts[0], 10, 64)
	var fraction int64
	if len(parts) == 2 {
		if len(parts[1]) == 0 || len(parts[1]) > 9 {
			return nil, nil, 0, fmt.Errorf("monthly allowance supports up to nine decimal places")
		}
		fraction, _ = strconv.ParseInt(parts[1]+strings.Repeat("0", 9-len(parts[1])), 10, 64)
	}
	limit := whole*1_000_000_000 + fraction
	if limit > 9_000_000_000_000_000 || p.Version < 0 || p.OrderVersion < 0 || p.Position < 1 {
		return nil, nil, 0, fmt.Errorf("invalid allowance, version, or position")
	}
	return &cost, expiry, limit, nil
}

func (s *Store) NodeProfiles() ([]NodeProfile, int64, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return nil, 0, err
	}
	defer tx.Rollback()
	var revision int64
	if err := tx.QueryRow("SELECT revision FROM admin_revision WHERE id=1").Scan(&revision); err != nil {
		return nil, 0, err
	}
	rows, err := tx.Query("SELECT " + agentColumns + " FROM agents ORDER BY display_order,id")
	if err != nil {
		return nil, 0, err
	}
	profiles := []NodeProfile{}
	for rows.Next() {
		a, err := scanAgent(rows)
		if err != nil {
			rows.Close()
			return nil, 0, err
		}
		profiles = append(profiles, nodeProfile(a, len(profiles)+1, revision, s.now()))
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, 0, err
	}
	return profiles, revision, tx.Commit()
}

func (s *Store) UpdateNodeProfile(id int64, p NodeProfileInput, actor string) error {
	cost, expiry, limit, err := p.normalize(s.now())
	if err != nil {
		return err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec("UPDATE agents SET last_seen=last_seen WHERE id=?", id); err != nil {
		return err
	}
	a, err := scanAgent(tx.QueryRow("SELECT "+agentColumns+" FROM agents WHERE id=?", id))
	if err != nil {
		return err
	}
	var revision int64
	if err := tx.QueryRow("SELECT revision FROM admin_revision WHERE id=1").Scan(&revision); err != nil {
		return err
	}
	if a.ProfileRevision != p.Version || revision != p.OrderVersion {
		return ErrProfileConflict
	}
	var oldPosition int
	if err := tx.QueryRow("SELECT COUNT(*) FROM agents WHERE display_order<? OR (display_order=? AND id<=?)", a.DisplayOrder, a.DisplayOrder, id).Scan(&oldPosition); err != nil {
		return err
	}
	before, _ := json.Marshal(nodeProfile(a, oldPosition, revision, s.now()))
	day := 1
	if a.TrafficResetDay != nil {
		day = *a.TrafficResetDay
	} else if legacyDay, configured := trafficResetDay(a.BillingPeriod, a.ExpiresAt); configured {
		day = legacyDay
	}
	encoded, _ := json.Marshal(cost)
	if _, err := tx.Exec(`UPDATE agents SET display_name=?,region=?,billing_cost=?,billing_period=?,expires_at=?,
 monthly_traffic_limit=?,profile_managed=1,profile_revision=profile_revision+1,traffic_reset_day=? WHERE id=?`,
		p.DisplayName, p.Region, string(encoded), p.BillingPeriod, expiry, limit, day, id); err != nil {
		return err
	}
	if err := moveAgentTx(tx, strconv.FormatInt(id, 10), p.Position); err != nil {
		return err
	}
	if err := bumpAdminRevision(tx); err != nil {
		return err
	}
	updated, err := scanAgent(tx.QueryRow("SELECT "+agentColumns+" FROM agents WHERE id=?", id))
	if err != nil {
		return err
	}
	after, _ := json.Marshal(nodeProfile(updated, p.Position, revision+1, s.now()))
	if _, err := tx.Exec("INSERT INTO node_profile_audit(agent_id,actor,changed_at,before_json,after_json) VALUES(?,?,?,?,?)", id, actor, s.now().Unix(), string(before), string(after)); err != nil {
		return err
	}
	if _, err := tx.Exec("DELETE FROM node_profile_audit WHERE id NOT IN (SELECT id FROM node_profile_audit ORDER BY id DESC LIMIT 200)"); err != nil {
		return err
	}
	return tx.Commit()
}
