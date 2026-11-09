package api

import (
	"errors"
	"net/http"

	"github.com/terastal-hq/prot/internal/sshconfig"
)

// sshConfigPath is the configuration file machine names are read from.
func (s *Server) sshConfigPath() (string, error) {
	if s.sshConfig != "" {
		return s.sshConfig, nil
	}
	return sshconfig.DefaultPath()
}

// handleListSSHHosts lists the machine names defined in the ssh configuration. It only reads files.
func (s *Server) handleListSSHHosts(w http.ResponseWriter, _ *http.Request) {
	path, err := s.sshConfigPath()
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	hosts, err := sshconfig.Hosts(path)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "reading "+path+": "+err.Error())
		return
	}
	jsonOK(w, map[string]any{"hosts": hosts, "count": len(hosts), "config_path": path})
}

// handleGetSSHHost says what ssh will use for one listed machine name.
func (s *Server) handleGetSSHHost(w http.ResponseWriter, r *http.Request) {
	path, err := s.sshConfigPath()
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	hosts, err := sshconfig.Hosts(path)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "reading "+path+": "+err.Error())
		return
	}
	if s.sshPath == "" {
		jsonErr(w, http.StatusNotImplemented, "ssh is not installed")
		return
	}

	// An explicit -F is only needed when the path is not ssh's own default.
	resolver := sshconfig.Resolver{SSHPath: s.sshPath, ConfigPath: s.sshConfig}
	resolved, err := resolver.Resolve(r.Context(), r.PathValue("alias"), hosts)
	switch {
	case errors.Is(err, sshconfig.ErrUnknownHost):
		jsonErr(w, http.StatusNotFound, err.Error())
	case err != nil:
		jsonErr(w, http.StatusInternalServerError, err.Error())
	default:
		jsonOK(w, resolved)
	}
}
