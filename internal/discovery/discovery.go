package discovery

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	gopsnet "github.com/shirou/gopsutil/v4/net"
	"github.com/shirou/gopsutil/v4/process"
)

// ProcessService represents an observed listening TCP process.
type ProcessService struct {
	ID           string    `json:"id"`
	PID          int32     `json:"pid"`
	Name         string    `json:"name"`
	Exe          string    `json:"exe"`
	Cmdline      string    `json:"cmdline"`
	Cwd          string    `json:"cwd"`
	Username     string    `json:"username"`
	CreateTime   int64     `json:"create_time"`
	IP           string    `json:"ip"`
	Port         uint32    `json:"port"`
	Family       string    `json:"family"`
	IsHTTP       bool      `json:"is_http"`
	HTTPStatus   int       `json:"http_status,omitempty"`
	HTTPServer   string    `json:"http_server,omitempty"`
	Framework    string    `json:"framework,omitempty"`
	PageTitle    string    `json:"page_title,omitempty"`
	DiscoveredAt time.Time `json:"discovered_at"`
	Fingerprint  string    `json:"fingerprint"`
}

// Config controls discovery behavior and protocol check bounds.
type Config struct {
	CheckHTTP    bool          `json:"check_http"`
	HTTPTimeout  time.Duration `json:"http_timeout"`
	MaxBodyBytes int64         `json:"max_body_bytes"`
	IgnorePorts  []uint32      `json:"ignore_ports"`
	ExcludePids  []int32       `json:"exclude_pids"`
}

// DefaultConfig provides conservative defaults.
func DefaultConfig() Config {
	return Config{
		CheckHTTP:    true,
		HTTPTimeout:  400 * time.Millisecond,
		MaxBodyBytes: 4096,
		IgnorePorts:  []uint32{0},
	}
}

// Discover scans local listening TCP sockets and identifies processes.
func Discover(ctx context.Context, cfg Config) ([]ProcessService, error) {
	if cfg.HTTPTimeout <= 0 {
		cfg.HTTPTimeout = 400 * time.Millisecond
	}
	if cfg.MaxBodyBytes <= 0 {
		cfg.MaxBodyBytes = 4096
	}

	conns, err := gopsnet.ConnectionsWithContext(ctx, "tcp")
	if err != nil {
		return nil, fmt.Errorf("listing network connections: %w", err)
	}

	ignoreMap := make(map[uint32]bool, len(cfg.IgnorePorts))
	for _, p := range cfg.IgnorePorts {
		ignoreMap[p] = true
	}
	excludePidMap := make(map[int32]bool, len(cfg.ExcludePids))
	for _, pid := range cfg.ExcludePids {
		excludePidMap[pid] = true
	}

	seenEndpoints := make(map[string]bool)
	rawServices := []ProcessService{}

	for _, c := range conns {
		if c.Status != "LISTEN" {
			continue
		}
		if ignoreMap[c.Laddr.Port] {
			continue
		}
		if excludePidMap[c.Pid] {
			continue
		}

		key := fmt.Sprintf("%s:%d", c.Laddr.IP, c.Laddr.Port)
		if seenEndpoints[key] {
			continue
		}
		seenEndpoints[key] = true

		svc := ProcessService{
			PID:          c.Pid,
			IP:           c.Laddr.IP,
			Port:         c.Laddr.Port,
			DiscoveredAt: time.Now().UTC(),
		}

		if strings.Contains(c.Laddr.IP, ":") {
			svc.Family = "IPv6"
		} else {
			svc.Family = "IPv4"
		}

		if c.Pid > 0 {
			enrichProcessDetails(ctx, &svc)
		}

		// Calculate stable fingerprint: PID + CreateTime + Exe + Port
		svc.Fingerprint = computeFingerprint(svc)
		svc.ID = fmt.Sprintf("disc_%s_%d", svc.Fingerprint[:12], svc.Port)

		rawServices = append(rawServices, svc)
	}

	if !cfg.CheckHTTP || len(rawServices) == 0 {
		return rawServices, nil
	}

	// Concurrent bounded HTTP checks
	var wg sync.WaitGroup
	results := make([]ProcessService, len(rawServices))
	sem := make(chan struct{}, 8) // Limit concurrency to avoid socket storms

	for i, s := range rawServices {
		wg.Add(1)
		go func(idx int, target ProcessService) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				results[idx] = target
				return
			}

			checkHTTPService(ctx, &target, cfg)
			results[idx] = target
		}(i, s)
	}

	wg.Wait()
	return results, nil
}

func enrichProcessDetails(ctx context.Context, s *ProcessService) {
	p, err := process.NewProcessWithContext(ctx, s.PID)
	if err != nil {
		return
	}

	if name, err := p.NameWithContext(ctx); err == nil {
		s.Name = name
	}
	if exe, err := p.ExeWithContext(ctx); err == nil {
		s.Exe = exe
	}
	if cmd, err := p.CmdlineWithContext(ctx); err == nil {
		s.Cmdline = cmd
	}
	if cwd, err := p.CwdWithContext(ctx); err == nil {
		s.Cwd = cwd
	}
	if user, err := p.UsernameWithContext(ctx); err == nil {
		s.Username = user
	}
	if ctime, err := p.CreateTimeWithContext(ctx); err == nil {
		s.CreateTime = ctime
	}
}

func computeFingerprint(s ProcessService) string {
	h := sha256.New()
	h.Write([]byte(fmt.Sprintf("%d:%d:%s:%d:%s", s.PID, s.CreateTime, s.Exe, s.Port, s.IP)))
	return hex.EncodeToString(h.Sum(nil))
}

var titleRegex = regexp.MustCompile(`(?i)<title[^>]*>(.*?)</title>`)

func checkHTTPService(ctx context.Context, s *ProcessService, cfg Config) {
	host := s.IP
	if host == "0.0.0.0" || host == "" || host == "::" {
		host = "127.0.0.1"
	} else if host == "::1" {
		host = "[::1]"
	}

	url := fmt.Sprintf("http://%s:%d/", host, s.Port)

	reqCtx, cancel := context.WithTimeout(ctx, cfg.HTTPTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, url, nil)
	if err != nil {
		return
	}

	req.Header.Set("User-Agent", "prot-discovery/1.0")
	req.Header.Set("Accept", "text/html,application/json,*/*")

	client := &http.Client{
		Timeout: cfg.HTTPTimeout,
		Transport: &http.Transport{
			DisableKeepAlives: true,
			DialContext: (&net.Dialer{
				Timeout: cfg.HTTPTimeout,
			}).DialContext,
		},
	}

	resp, err := client.Do(req)
	if err != nil {
		return
	}
	defer resp.Body.Close()

	s.IsHTTP = true
	s.HTTPStatus = resp.StatusCode
	s.HTTPServer = resp.Header.Get("Server")
	poweredBy := resp.Header.Get("X-Powered-By")

	// Read limited body for title and framework detection
	limitedReader := io.LimitReader(resp.Body, cfg.MaxBodyBytes)
	bodyBytes, _ := io.ReadAll(limitedReader)
	bodyStr := string(bodyBytes)

	if matches := titleRegex.FindStringSubmatch(bodyStr); len(matches) > 1 {
		s.PageTitle = strings.TrimSpace(matches[1])
	}

	// Detect framework heuristics
	s.Framework = detectFramework(
		resp.Header,
		bodyStr,
		poweredBy,
		s.Cmdline,
	)
}

func detectFramework(headers http.Header, body, poweredBy, cmdline string) string {
	if strings.Contains(poweredBy, "Next.js") || strings.Contains(body, "__NEXT_DATA__") {
		return "Next.js"
	}
	if strings.Contains(body, "/@vite/client") || strings.Contains(cmdline, "vite") {
		return "Vite"
	}
	if strings.Contains(body, "react-root") || strings.Contains(body, `id="root"`) {
		return "React"
	}
	if strings.Contains(body, "svelte") {
		return "Svelte"
	}
	if strings.Contains(body, "nuxt") {
		return "Nuxt"
	}
	if strings.Contains(body, "vue") {
		return "Vue"
	}
	if poweredBy != "" {
		return poweredBy
	}
	if server := headers.Get("Server"); server != "" {
		return server
	}
	return ""
}
