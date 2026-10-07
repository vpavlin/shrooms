package agent

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The Venice keys of pi's settings are asked after — one written out and one
// from the environment, each once — and reported by fingerprint with their
// balances and when the allowance refills; never the key.
func TestCreditsOfVeniceKeys(t *testing.T) {
	var asked []string
	venice := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked = append(asked, r.Header.Get("Authorization"))
		if r.URL.Path != "/api/v1/api_keys/rate_limits" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Authorization") == "Bearer bad" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Write([]byte(`{"data":{"accessPermitted":true,"balances":{"USD":-0.03,"DIEM":5.62},"nextEpochBegins":"2026-10-08T00:00:00.000Z"}}`))
	}))
	defer venice.Close()
	dir := t.TempDir()
	t.Setenv("PI_CODING_AGENT_DIR", dir)
	t.Setenv("VENICE_INFERENCE_KEY", "from-env")
	os.WriteFile(filepath.Join(dir, "models.json"), []byte(`{"providers":{
		"venice":{"baseUrl":"`+venice.URL+`/api/v1","apiKey":"$VENICE_INFERENCE_KEY"},
		"venice2":{"baseUrl":"https://api.venice.ai/x","apiKey":"from-env"},
		"local":{"baseUrl":"http://localhost:11434/v1","apiKey":"ollama"}}}`), 0o600)
	keys := piCreditKeys()
	if len(keys) != 1 || keys[0].key != "from-env" {
		t.Fatalf("keys %+v", keys)
	}
	c := veniceCredit(context.Background(), creditKey{"venice", venice.URL + "/api/v1", "from-env"})
	if c.Error != "" || c.Balances["DIEM"] != 5.62 || c.ResetsAt.Hour() != 0 || c.Key != fingerprint("from-env") || strings.Contains(c.Key, "from-env") {
		t.Fatalf("credit %+v", c)
	}
	if bad := veniceCredit(context.Background(), creditKey{"venice", venice.URL + "/api/v1", "bad"}); !strings.Contains(bad.Error, "401") {
		t.Errorf("a refused key: %+v", bad)
	}
	if asked[0] != "Bearer from-env" {
		t.Errorf("asked with %q", asked[0])
	}
}
