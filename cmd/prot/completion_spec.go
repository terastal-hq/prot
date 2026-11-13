package main

// The table below describes the command line for shell completion: which commands exist, what each takes as
// arguments and which flags it accepts. It is the only thing the completion engine knows about prot's syntax, so
// when a command or flag is added, this is where it has to be added too. completion_test.go parses this package's
// source and fails when the two disagree.

// valueKind says what follows a flag, which decides how a shell completes it.
type valueKind int

const (
	valueNone valueKind = iota // a switch: it takes no value
	valueText                  // free text: nothing to suggest
	valueFile                  // a path to a file
	valueDir                   // a path to a directory
	valueHost                  // a machine name from ~/.ssh/config
)

// listKind says where a list of suggestions comes from.
type listKind int

const (
	listNone       listKind = iota // no argument here: flags may follow
	listName                       // a name the user chooses: nothing to suggest, and flags do not belong here yet
	listServices                   // aliases of the services the daemon knows
	listTunnels                    // aliases of the saved SSH tunnels
	listDiscovered                 // ids of the processes prot has discovered
	listHosts                      // machines defined in ~/.ssh/config
	listShells                     // shells prot can print a completion script for
)

type flagSpec struct {
	Name   string // long form, such as "--json"
	Short  string // optional short form, such as "-n"
	Value  valueKind
	Repeat bool // may be given more than once
	Help   string
}

type commandSpec struct {
	Name    string
	Aliases []string
	Help    string
	Args    []listKind // what each leading positional argument is; later ones are not completed
	Flags   []flagSpec
	Subs    []commandSpec
}

var jsonFlag = flagSpec{Name: "--json", Help: "Print JSON instead of a table"}

// tunnelSettings are the options 'tunnel add' and 'tunnel edit' share.
func tunnelSettings() []flagSpec {
	return []flagSpec{
		{Name: "--host", Value: valueHost, Help: "SSH server: host, user@host or a machine from ~/.ssh/config"},
		{Name: "--user", Value: valueText, Help: "SSH user"},
		{Name: "--remote-host", Value: valueText, Help: "Address on the remote side"},
		{Name: "--remote-port", Value: valueText, Help: "Port to reach on the remote side"},
		{Name: "--local-port", Value: valueText, Help: "Local port (0 picks one)"},
		{Name: "--identity", Value: valueFile, Help: "Private key file"},
		{Name: "--ssh-port", Value: valueText, Help: "SSH server port"},
		{Name: "--ssh-arg", Value: valueText, Repeat: true, Help: "Extra ssh argument"},
		{Name: "--auto-connect", Help: "Connect now and every time protd starts"},
	}
}

func tunnelAddFlags() []flagSpec {
	return append(tunnelSettings(),
		flagSpec{Name: "--password", Help: "Ask for an SSH password and keep it in the OS keyring"},
		flagSpec{Name: "--password-stdin", Help: "Read the password from standard input"},
	)
}

func tunnelEditFlags() []flagSpec {
	return append([]flagSpec{{Name: "--alias", Value: valueText, Help: "New .localhost alias"}}, tunnelSettings()...)
}

// serviceSettings are the options 'add' and 'edit' share.
func serviceSettings() []flagSpec {
	return []flagSpec{
		{Name: "--cmd", Value: valueText, Help: "Command to run"},
		{Name: "--port", Value: valueText, Help: "Service port"},
		{Name: "--cwd", Value: valueDir, Help: "Working directory"},
		{Name: "--env", Value: valueText, Repeat: true, Help: "Environment variable, KEY=VALUE"},
		{Name: "--restart-policy", Value: valueText, Help: "no, on-failure or always"},
		{Name: "--auto-port", Help: "Assign a free port automatically"},
		{Name: "--auto-start", Help: "Start the service whenever protd starts"},
	}
}

var tunnelSpecs = []commandSpec{
	{Name: "add", Help: "Save an SSH tunnel", Args: []listKind{listName}, Flags: tunnelAddFlags()},
	{Name: "ls", Aliases: []string{"list"}, Help: "List tunnels and their state", Flags: []flagSpec{jsonFlag}},
	{Name: "edit", Help: "Change a tunnel's settings in place", Args: []listKind{listTunnels}, Flags: tunnelEditFlags()},
	{
		Name: "start", Help: "Connect a tunnel", Args: []listKind{listTunnels},
		Flags: []flagSpec{{Name: "--no-wait", Help: "Return without waiting for the connection"}},
	},
	{Name: "stop", Help: "Disconnect a tunnel", Args: []listKind{listTunnels}},
	{
		Name: "rm", Aliases: []string{"remove", "delete"}, Help: "Disconnect and delete a tunnel",
		Args: []listKind{listTunnels},
	},
	{
		Name: "password", Aliases: []string{"passwd"}, Help: "Set, replace or remove the stored password",
		Args: []listKind{listTunnels},
		Flags: []flagSpec{
			{Name: "--stdin", Help: "Read the password from standard input"},
			{Name: "--password-stdin", Help: "Read the password from standard input"},
			{Name: "--clear", Help: "Remove the stored password"},
		},
	},
	{Name: "help", Aliases: []string{"--help", "-h"}, Help: "Show the tunnel usage"},
}

var sshSpecs = []commandSpec{
	{Name: "ls", Aliases: []string{"list"}, Help: "List the machines defined in ~/.ssh/config"},
	{Name: "help", Aliases: []string{"--help", "-h"}, Help: "Show the ssh usage"},
}

var commandSpecs = []commandSpec{
	{
		Name: "ls", Aliases: []string{"list"}, Help: "List all registered and discovered services",
		Flags: []flagSpec{jsonFlag},
	},
	{Name: "inspect", Help: "Show detailed configuration and runtime state", Args: []listKind{listServices}},
	{
		Name: "add", Help: "Register a new managed service", Args: []listKind{listName},
		Flags: append(serviceSettings(),
			flagSpec{Name: "--alias", Value: valueText, Help: "Local .localhost alias"},
		),
	},
	{
		Name: "edit", Help: "Change a registered service", Args: []listKind{listServices},
		Flags: append(serviceSettings(),
			flagSpec{Name: "--name", Value: valueText, Help: "New display name"},
			flagSpec{Name: "--alias", Value: valueText, Help: "New .localhost alias"},
			flagSpec{Name: "--unset-env", Value: valueText, Repeat: true, Help: "Remove an environment variable"},
			flagSpec{Name: "--clear-env", Help: "Remove every environment variable first"},
		),
	},
	{Name: "start", Help: "Start a managed service", Args: []listKind{listServices}},
	{Name: "stop", Help: "Stop a running managed service", Args: []listKind{listServices}},
	{Name: "restart", Help: "Restart a managed service", Args: []listKind{listServices}},
	{
		Name: "rm", Aliases: []string{"remove", "delete"}, Help: "Remove a service registration",
		Args: []listKind{listServices},
	},
	{
		Name: "logs", Help: "View or stream service stdout/stderr logs", Args: []listKind{listServices},
		Flags: []flagSpec{
			{Name: "--lines", Short: "-n", Value: valueText, Help: "Number of lines to show"},
			{Name: "--follow", Short: "-f", Help: "Keep streaming new lines"},
		},
	},
	{
		Name: "adopt", Help: "Adopt an observed process into a managed service", Args: []listKind{listDiscovered},
		Flags: []flagSpec{
			{Name: "--name", Value: valueText, Help: "Service name"},
			{Name: "--alias", Value: valueText, Help: "Service alias"},
			{Name: "--cmd", Value: valueText, Help: "Launch command"},
		},
	},
	{
		Name: "discover", Help: "Scan listening TCP processes and HTTP services",
		Flags: []flagSpec{jsonFlag},
	},
	{Name: "alias", Help: "Map a .localhost alias to an upstream target URL"},
	{Name: "tunnel", Aliases: []string{"tunnels"}, Help: "Manage SSH tunnels", Subs: tunnelSpecs},
	{Name: "ssh", Help: "List the machines in your ~/.ssh/config", Subs: sshSpecs},
	{Name: "status", Help: "Show protd daemon health and active routes"},
	{Name: "doctor", Help: "Check environment, permissions, and toolchain"},
	{
		Name: "update", Aliases: []string{"upgrade"}, Help: "Check for updates and upgrade prot in-place",
		Flags: []flagSpec{
			{Name: "--check", Short: "-c", Help: "Only report whether an update exists"},
			{Name: "--force", Short: "-f", Help: "Reinstall even when up to date"},
		},
	},
	{Name: "mcp", Help: "Run the Model Context Protocol (MCP) stdio adapter"},
	{Name: "completion", Help: "Print the shell completion script", Args: []listKind{listShells}},
	{Name: "version", Aliases: []string{"--version", "-v"}, Help: "Print version information"},
	{Name: "help", Aliases: []string{"--help", "-h"}, Help: "Show usage"},
}
