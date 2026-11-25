package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func buildSample(t *testing.T, pkg string) string {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping build-backed test in short mode")
	}
	out := filepath.Join(t.TempDir(), pkg)
	cmd := exec.Command("go", "build", "-o", out, "github.com/cansarihan/rift/deploy/sample/"+pkg)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("go build failed: %v\n%s", err, output)
	}
	return out
}

func newTestServer(token, old, updated string) http.Handler {
	return New(Options{
		Old:   old,
		New:   updated,
		Token: token,
		UI:    false,
	}, BuildInfo{Version: "test", Commit: "abc", GoVersion: "go1", Platform: "test/arch"}).Handler()
}

func TestHealthz(t *testing.T) {
	srv := httptest.NewServer(newTestServer("", "x", "x"))
	defer srv.Close()
	res, err := http.Get(srv.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("healthz = %d", res.StatusCode)
	}
}

func TestTokenGuard(t *testing.T) {
	srv := httptest.NewServer(newTestServer("secret", "x", "x"))
	defer srv.Close()

	res, err := http.Get(srv.URL + "/api/v1/status")
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status without token = %d, want 401", res.StatusCode)
	}

	res2, err := http.Get(srv.URL + "/api/v1/status?token=secret")
	if err != nil {
		t.Fatal(err)
	}
	_ = res2.Body.Close()
	if res2.StatusCode != http.StatusOK {
		t.Fatalf("status with token = %d, want 200", res2.StatusCode)
	}
}

func TestScanRealDiff(t *testing.T) {
	old := buildSample(t, "old")
	updated := buildSample(t, "new")
	srv := httptest.NewServer(newTestServer("", old, updated))
	defer srv.Close()

	res, err := http.Post(srv.URL+"/api/v1/scans", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	var created Scan
	_ = json.NewDecoder(res.Body).Decode(&created)
	_ = res.Body.Close()
	if created.ID == "" {
		t.Fatal("scan was not created")
	}

	deadline := time.Now().Add(30 * time.Second)
	var scan Scan
	for time.Now().Before(deadline) {
		r, err := http.Get(srv.URL + "/api/v1/scans/" + created.ID)
		if err != nil {
			t.Fatal(err)
		}
		_ = json.NewDecoder(r.Body).Decode(&scan)
		_ = r.Body.Close()
		if scan.Status == "done" || scan.Status == "error" {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	if scan.Status != "done" {
		t.Fatalf("scan did not finish cleanly: status=%q err=%q", scan.Status, scan.Error)
	}
	if scan.Compared == 0 {
		t.Error("expected a positive compared-function count")
	}
	if scan.Flagged == 0 {
		t.Error("expected the hardened sample to flag at least one function")
	}
	found := false
	for _, d := range scan.Deltas {
		if d.Name == "main.parseRecord" && (d.Band == "likely" || d.Band == "possible") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected main.parseRecord to be flagged; deltas=%d band=%q", len(scan.Deltas), scan.Band)
	}
}

func TestScanNotFound(t *testing.T) {
	srv := httptest.NewServer(newTestServer("", "x", "x"))
	defer srv.Close()
	res, err := http.Get(srv.URL + "/api/v1/scans/nope")
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("missing scan = %d, want 404", res.StatusCode)
	}
}
