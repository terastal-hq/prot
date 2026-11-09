package main

import (
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/terastal-hq/prot/internal/sshconfig"
)

const sshUsage = `Usage: prot ssh <command>

Commands:
  ls    List the machines defined in your ~/.ssh/config and what ssh resolves for each

Use one as a tunnel's --host: 'prot tunnel add db --host prod-db --remote-port 5432'.`

func cmdSSH(args []string) {
	if len(args) == 0 {
		fmt.Println(sshUsage)
		return
	}
	switch args[0] {
	case "ls", "list":
		sshList()
	case "help", "--help", "-h":
		fmt.Println(sshUsage)
	default:
		fatalf("Unknown command %q\n%s", args[0], sshUsage)
	}
}

// sshHostList is the response of GET /v1/ssh/hosts.
type sshHostList struct {
	Hosts      []string `json:"hosts"`
	ConfigPath string   `json:"config_path"`
}

func listSSHHosts(client *daemonClient) (sshHostList, error) {
	var list sshHostList
	err := client.call(http.MethodGet, "/v1/ssh/hosts", nil, &list, http.StatusOK)
	return list, err
}

// resolveSSHHost asks the daemon what ssh will use for a configured machine name.
func resolveSSHHost(client *daemonClient, alias string) (sshconfig.Resolved, error) {
	var res sshconfig.Resolved
	err := client.call(http.MethodGet, "/v1/ssh/hosts/"+url.PathEscape(alias), nil, &res, http.StatusOK)
	return res, err
}

func sshList() {
	client := connect()
	list, err := listSSHHosts(client)
	if err != nil {
		fatalf("Failed to read the ssh configuration: %v", err)
	}
	if len(list.Hosts) == 0 {
		fmt.Printf("No machines defined in %s.\n", list.ConfigPath)
		fmt.Println("Add a 'Host <name>' block there and it will show up here and in the desktop app.")
		return
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)
	_, _ = fmt.Fprintln(w, "NAME\tHOST\tUSER\tPORT\tKEY")
	_, _ = fmt.Fprintln(w, "----\t----\t----\t----\t---")
	for _, name := range list.Hosts {
		res, err := resolveSSHHost(client, name)
		if err != nil {
			_, _ = fmt.Fprintf(w, "%s\t(could not resolve: %v)\n", name, err)
			continue
		}
		_, _ = fmt.Fprintf(w, "%s\t%s\t%s\t%d\t%s\n", name, describeHost(res), res.User, res.Port, describeKeys(res))
	}
	_ = w.Flush()
}

func describeHost(res sshconfig.Resolved) string {
	if res.ProxyJump != "" {
		return res.HostName + " via " + res.ProxyJump
	}
	return res.HostName
}

func describeKeys(res sshconfig.Resolved) string {
	if len(res.IdentityFiles) == 0 {
		return "agent/default"
	}
	return strings.Join(res.IdentityFiles, ", ")
}

// noteConfiguredHost tells the user when a tunnel's --host is a machine from ~/.ssh/config, and what that means.
// It is only a courtesy: any failure is silent, since ssh does the real work.
func noteConfiguredHost(client *daemonClient, host string) {
	list, err := listSSHHosts(client)
	if err != nil || !sshconfig.Contains(list.Hosts, host) {
		return
	}
	res, err := resolveSSHHost(client, host)
	if err != nil {
		return
	}
	fmt.Printf("  Machine: %s is in %s (%s as %s, port %d)\n", host, list.ConfigPath, res.HostName, res.User, res.Port)
}
