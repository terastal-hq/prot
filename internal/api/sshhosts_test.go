package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
)

func sshConfigFile(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config")
	body := `Host prod
  HostName db.example.com
  User deploy
  Port 2222
  IdentityFile ~/.ssh/prod_key

Host staging

Host *
  ServerAliveInterval 30
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func getJSON(t *testing.T, srv *Server, path string, into any) int {
	t.Helper()
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	if into != nil && rec.Code == http.StatusOK {
		if err := json.Unmarshal(rec.Body.Bytes(), into); err != nil {
			t.Fatalf("%s: %v: %s", path, err, rec.Body.String())
		}
	}
	return rec.Code
}

func TestListSSHHosts(t *testing.T) {
	srv := NewServer(Config{SSHConfigPath: sshConfigFile(t)})
	var out struct {
		Hosts      []string `json:"hosts"`
		Count      int      `json:"count"`
		ConfigPath string   `json:"config_path"`
	}
	if code := getJSON(t, srv, "/v1/ssh/hosts", &out); code != http.StatusOK {
		t.Fatalf("status %d", code)
	}
	if !reflect.DeepEqual(out.Hosts, []string{"prod", "staging"}) || out.Count != 2 || out.ConfigPath == "" {
		t.Errorf("got %+v; wildcard blocks must not be listed", out)
	}
}

func TestListSSHHostsWithNoConfigIsEmpty(t *testing.T) {
	srv := NewServer(Config{SSHConfigPath: filepath.Join(t.TempDir(), "absent")})
	var out struct {
		Hosts []string `json:"hosts"`
	}
	if code := getJSON(t, srv, "/v1/ssh/hosts", &out); code != http.StatusOK || out.Hosts == nil || len(out.Hosts) != 0 {
		t.Errorf("status %d, hosts %v; want 200 and an empty list", code, out.Hosts)
	}
}

func TestGetSSHHost(t *testing.T) {
	sshPath, err := exec.LookPath("ssh")
	if err != nil {
		t.Skip("ssh is not installed")
	}
	srv := NewServer(Config{SSHConfigPath: sshConfigFile(t), SSHPath: sshPath})

	var got struct {
		Alias         string   `json:"alias"`
		HostName      string   `json:"hostname"`
		User          string   `json:"user"`
		Port          uint32   `json:"port"`
		IdentityFiles []string `json:"identity_files"`
	}
	if code := getJSON(t, srv, "/v1/ssh/hosts/prod", &got); code != http.StatusOK {
		t.Fatalf("status %d", code)
	}
	if got.HostName != "db.example.com" || got.User != "deploy" || got.Port != 2222 ||
		!reflect.DeepEqual(got.IdentityFiles, []string{"~/.ssh/prod_key"}) {
		t.Errorf("got %+v", got)
	}

	if code := getJSON(t, srv, "/v1/ssh/hosts/example.com", nil); code != http.StatusNotFound {
		t.Errorf("a name the config does not define: status %d, want 404", code)
	}
}

func TestGetSSHHostWithoutSSH(t *testing.T) {
	srv := NewServer(Config{SSHConfigPath: sshConfigFile(t)})
	if code := getJSON(t, srv, "/v1/ssh/hosts/prod", nil); code != http.StatusNotImplemented {
		t.Errorf("status %d, want 501", code)
	}
}
