package agent

// Credits are what a pay-as-you-go provider says is left on the keys this
// machine's sessions use: Venice's balances — DIEM, the daily allowance, and
// USD — and when the allowance refills. Asked of the provider every few
// minutes and reported with the usage (GET /v1/usage, GET /v1/sessions), as
// Claude Code's plan limits are; a key shared by several machines is one
// fingerprint, so the apps show it once.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Credit is one key's standing with its provider.
type Credit struct {
	Provider string             `json:"provider"` // "venice"
	Key      string             `json:"key"`      // a fingerprint of the key, never the key
	Balances map[string]float64 `json:"balances,omitempty"`
	ResetsAt time.Time          `json:"resets_at,omitempty"` // when the daily allowance (DIEM) refills
	At       time.Time          `json:"at"`
	Error    string             `json:"error,omitempty"`
	// At the pace of the last two hours (forecast.go): when DIEM runs out,
	// if before the refill, and how much is left at the refill.
	RunsOutAt    *time.Time `json:"runs_out_at,omitempty"`
	LeftAtRefill *float64   `json:"left_at_refill,omitempty"`
}

// creditKey is a key to ask about, where pi's settings name it.
type creditKey struct {
	provider, base, key string
}

func fingerprint(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:4])
}

// piCreditKeys are the Venice keys in pi's model settings
// (~/.pi/agent/models.json): an apiKey written out, or "$VAR"/"${VAR}" taken
// from this process's environment — the agent's, which is the sessions'.
func piCreditKeys() []creditKey {
	dir := os.Getenv("PI_CODING_AGENT_DIR")
	if dir == "" {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".pi", "agent")
	}
	b, err := os.ReadFile(filepath.Join(dir, "models.json"))
	if err != nil {
		return nil
	}
	var cfg struct {
		Providers map[string]struct {
			BaseURL string `json:"baseUrl"`
			APIKey  string `json:"apiKey"`
		} `json:"providers"`
	}
	if json.Unmarshal(b, &cfg) != nil {
		return nil
	}
	var out []creditKey
	seen := map[string]bool{}
	for _, p := range cfg.Providers {
		if !strings.Contains(p.BaseURL, "api.venice.ai") {
			continue
		}
		key := p.APIKey
		if strings.HasPrefix(key, "$") {
			key = os.Getenv(strings.Trim(strings.TrimPrefix(key, "$"), "{}"))
		}
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, creditKey{"venice", strings.TrimRight(p.BaseURL, "/"), key})
	}
	return out
}

// veniceCredit asks Venice where a key stands.
func veniceCredit(ctx context.Context, k creditKey) Credit {
	c := Credit{Provider: k.provider, Key: fingerprint(k.key), At: time.Now()}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, k.base+"/api_keys/rate_limits", nil)
	req.Header.Set("Authorization", "Bearer "+k.key)
	resp, err := (&http.Client{Timeout: 20 * time.Second}).Do(req)
	if err != nil {
		c.Error = err.Error()
		return c
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		c.Error = "venice: " + resp.Status
		return c
	}
	var d struct {
		Data struct {
			Balances        map[string]float64 `json:"balances"`
			NextEpochBegins time.Time          `json:"nextEpochBegins"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&d); err != nil {
		c.Error = fmt.Sprintf("venice: %v", err)
		return c
	}
	c.Balances = d.Data.Balances
	c.ResetsAt = d.Data.NextEpochBegins
	return c
}

// creditsEvery is how often the keys are asked about.
var creditsEvery = 5 * time.Minute

// WatchCredits asks after the keys pi's settings name, now and every few
// minutes, until ctx ends. The keys are read again each time: one added or
// changed is picked up without a restart.
func (m *Manager) WatchCredits(ctx context.Context) {
	go func() {
		for {
			var got []Credit
			for _, k := range piCreditKeys() {
				got = append(got, veniceCredit(ctx, k))
			}
			sort.Slice(got, func(i, j int) bool { return got[i].Key < got[j].Key })
			m.credits.Lock()
			if m.credits.points == nil {
				m.credits.points = map[string][]creditPoint{}
			}
			for i, c := range got {
				if d, ok := c.Balances["DIEM"]; ok && c.Error == "" {
					ps := append(m.credits.points[c.Key], creditPoint{at: c.At, diem: d, resetsAt: c.ResetsAt})
					for len(ps) > 0 && c.At.Sub(ps[0].at) > 26*time.Hour {
						ps = ps[1:]
					}
					m.credits.points[c.Key] = ps
				}
				got[i] = projectCredit(c, m.credits.points[c.Key], c.At)
			}
			m.credits.c = got
			m.credits.Unlock()
			select {
			case <-ctx.Done():
				return
			case <-time.After(creditsEvery):
			}
		}
	}()
}

// Credits is the newest standing of each key.
func (m *Manager) Credits() []Credit {
	m.credits.Lock()
	defer m.credits.Unlock()
	return append([]Credit{}, m.credits.c...)
}
