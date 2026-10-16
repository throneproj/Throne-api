// Command thronectl controls Throne through the Throne API.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"runtime/debug"
	"strconv"
	"strings"
	"time"

	throne "github.com/throneproj/Throne-api"
	"github.com/throneproj/Throne-api/internal/rawbody"
)

// version is set by release builds with -ldflags "-X main.version=v1.2.3".
var version = "dev"

func main() {
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr, os.Getenv))
}

type command struct {
	name string
	args string
	help string
	run  func(c *cli, fs *flag.FlagSet, args []string) error
}

func (cmd *command) synopsis() string {
	return strings.TrimSpace("thronectl " + cmd.name + " " + cmd.args)
}

// summary is the first sentence of the help, without its period.
func (cmd *command) summary() string {
	first, _, _ := strings.Cut(cmd.help, ". ")
	return strings.TrimSuffix(first, ".")
}

var commands []*command

func init() {
	commands = []*command{
		{"status", "", "Show the connection state.", cmdStatus},
		{"groups", "", "List the groups.", cmdGroups},
		{"profiles", "[--group N] [--name REGEX] [--ids 1,2]", "List profiles.", cmdProfiles},
		{"connect", "<id> | last | best [--group N] [--name REGEX] [--max-age 1h] [--restart] [--no-wait]",
			"Start a profile: by ID, the last one, or the one with the lowest latency.", cmdConnect},
		{"disconnect", "[--no-wait]", "Stop the running profile.", cmdDisconnect},
		{"toggle", "[--no-wait]", "Connect the last profile when idle, else disconnect.", cmdToggle},
		{"tun", "on | off | toggle [--no-wait]", "Switch Tun mode.", cmdTun},
		{"proxy", "on | off | toggle [--no-wait]", "Switch the system proxy.", cmdProxy},
		{"routes", "", "List the routing profiles.", cmdRoutes},
		{"route", "<id> [--no-wait]", "Activate a routing profile.", cmdRoute},
		{"test", "latency | ip | speed (--group N [--name REGEX] | <id>...) [--no-wait]",
			"Test profiles and show the results. Tests can take minutes: raise --timeout, or use --no-wait.", cmdTest},
		{"jobs", "", "List the recent tests.", cmdJobs},
		{"job", "<id>", "Show a test and its results.", cmdJob},
		{"cancel", "<id>", "Stop a test.", cmdCancel},
		{"clear", "(--group N [--name REGEX] | <id>...)", "Clear the test results of profiles.", cmdClear},
		{"live", "", "Show the current traffic rates.", cmdLive},
		{"traffic", "[--by profile | app] [--since 24h]", "Show traffic totals.", cmdTraffic},
		{"import", "[--group N] (FILE | - | --url URL) [--no-wait]",
			"Import profiles from a file, standard input or a URL. A file or standard input holds share links, a " +
				"subscription body, a Clash, sing-box or Xray configuration, or a WireGuard or OpenVPN file, of at " +
				"most 900 KiB; with --url Throne downloads the URL once.", cmdImport},
		{"sub", "add <url> [--name NAME] [--no-auto-update] [--no-update] | update (<id>... | --all) [--no-wait]",
			"Add a subscription group, or update subscriptions. A failed update makes the exit status 1.", cmdSub},
		{"group", "add <name> | rm <id> [--no-wait]", "Add a group, or delete one with its profiles.", cmdGroup},
		{"profile", "rm (<id>... | --group N [--name REGEX]) [--no-wait]", "Delete profiles.", cmdProfile},
		{"settings", "[get] [KEY...] | set KEY=VALUE...",
			"Show or change settings. A VALUE of true, false or a number is sent as one, anything else as text.", cmdSettings},
		{"backup", "create [-o FILE] [--parts LIST] | restore FILE [--parts LIST] [--yes]",
			"Back up Throne's data to a file, or restore it from one. Archives hold credentials in plain text. " +
				"LIST is comma-separated parts: profiles, routes, settings, otp, ip_lists and icons. restore " +
				"shows the archive and asks before it replaces anything; Throne then restarts.", cmdBackup},
		{"rulesets", "update [--no-wait]",
			"Update the remote rule-sets of the running profile. A failed rule-set makes the exit status 1.", cmdRuleSets},
		{"geo", "[status] | update [geoip | geosite]... [--only-missing] [--no-wait]",
			"Show or update the geoip and geosite files. Downloads can take minutes: raise --timeout, or use " +
				"--no-wait. A failed file makes the exit status 1.", cmdGeo},
		{"restart", "", "Restart Throne.", cmdRestart},
		{"shutdown", "[--yes]", "Make Throne exit. It asks first, unless --yes is given.", cmdShutdown},
		{"keygen", "[-o FILE]", "Generate a new Throne API key. It does not connect to Throne.", cmdKeygen},
		{"call", "<METHOD> <PATH> [key=value ...] [--body JSON]",
			"Send any request and print the response body.", cmdCall},
		{"completion", "bash | zsh | fish | powershell",
			"Print a shell completion script. It completes commands, flags and arguments, and the IDs of profiles, " +
				"groups, routing profiles and tests when Throne answers within a second, at the address and with " +
				"the key that other commands use. To load it:\n\n" +
				"  bash        add this line to ~/.bashrc, or to ~/.bash_profile on macOS:\n" +
				"                eval \"$(thronectl completion bash)\"\n" +
				"              or save the script as thronectl in bash-completion's directory, such as\n" +
				"              ~/.local/share/bash-completion/completions\n" +
				"  zsh         add these lines to ~/.zshrc:\n" +
				"                autoload -Uz compinit && compinit\n" +
				"                source <(thronectl completion zsh)\n" +
				"              or save the script as _thronectl in a directory that ~/.zshrc adds to $fpath\n" +
				"              before it runs compinit\n" +
				"  fish        save the script as ~/.config/fish/completions/thronectl.fish\n" +
				"  powershell  add this line to $PROFILE:\n" +
				"                thronectl completion powershell | Out-String | Invoke-Expression", cmdCompletion},
		{"version", "", "Print the version.", cmdVersion},
	}
}

type cli struct {
	stdin          io.Reader
	stdout, stderr io.Writer
	getenv         func(string) string

	addr    string
	keyFile string
	json    bool
	timeout time.Duration

	cmd    *command
	raw    json.RawMessage
	cancel context.CancelFunc
}

func run(args []string, stdin io.Reader, stdout, stderr io.Writer, getenv func(string) string) int {
	c := &cli{stdin: stdin, stdout: stdout, stderr: stderr, getenv: getenv, addr: getenv("THRONE_ADDR"),
		timeout: 30 * time.Second}
	if c.addr == "" {
		c.addr = throne.DefaultAddr
	}
	defer func() {
		if c.cancel != nil {
			c.cancel()
		}
	}()
	if len(args) > 0 && args[0] == completeCommand {
		c.completeWords(args[1:])
		return 0
	}
	return c.exit(c.main(args))
}

func (c *cli) main(args []string) error {
	fs := flag.NewFlagSet("thronectl", flag.ContinueOnError)
	fs.SetOutput(c.stderr)
	fs.Usage = func() { c.usage(fs.Output()) }
	c.globalFlags(fs)
	if err := fs.Parse(args); err != nil {
		return parseError(err)
	}
	args = fs.Args()
	if len(args) == 0 {
		c.usage(c.stderr)
		return errReported
	}
	if args[0] == "help" {
		return c.help(args[1:])
	}
	for _, cmd := range commands {
		if cmd.name == args[0] {
			return c.runCommand(cmd, args[1:], c.stderr)
		}
	}
	return usageError{msg: fmt.Sprintf("unknown command %q; run 'thronectl help' for the list", args[0])}
}

func (c *cli) runCommand(cmd *command, args []string, out io.Writer) error {
	c.cmd = cmd
	fs := flag.NewFlagSet("thronectl "+cmd.name, flag.ContinueOnError)
	fs.SetOutput(out)
	fs.Usage = func() {
		w := fs.Output()
		fmt.Fprintf(w, "Usage: %s\n\n%s\n", cmd.synopsis(), cmd.help)
		own := flag.NewFlagSet("", flag.ContinueOnError)
		own.SetOutput(w)
		n := 0
		fs.VisitAll(func(f *flag.Flag) {
			if !globalFlags[f.Name] {
				own.Var(f.Value, f.Name, f.Usage)
				own.Lookup(f.Name).DefValue = f.DefValue
				n++
			}
		})
		if n > 0 {
			fmt.Fprintln(w, "\nFlags:")
			own.PrintDefaults()
		}
		fmt.Fprintln(w, "\nThe global flags --addr, --key-file, --json and --timeout work here too; see 'thronectl help'.")
	}
	return cmd.run(c, fs, args)
}

var globalFlags = map[string]bool{"addr": true, "key-file": true, "json": true, "timeout": true}

func (c *cli) help(args []string) error {
	if len(args) == 0 {
		c.usage(c.stdout)
		return nil
	}
	for _, cmd := range commands {
		if cmd.name == args[0] {
			return c.runCommand(cmd, []string{"-h"}, c.stdout)
		}
	}
	return usageError{msg: fmt.Sprintf("unknown command %q", args[0])}
}

func (c *cli) usage(w io.Writer) {
	fmt.Fprint(w, `thronectl controls Throne through the Throne API.

Usage: thronectl [flags] <command> [arguments]

Commands:
`)
	for _, cmd := range commands {
		fmt.Fprintf(w, "  %-11s %s.\n", cmd.name, cmd.summary())
	}
	fmt.Fprint(w, `
The key comes from THRONE_KEY, the file THRONE_KEY_FILE names, or --key-file. In Throne,
Preferences > Integration Settings > Throne API > Copy for scripts copies THRONE_ADDR and
THRONE_KEY.

Flags (also accepted after the command):
`)
	fs := flag.NewFlagSet("", flag.ContinueOnError)
	fs.SetOutput(w)
	c.globalFlags(fs)
	fs.PrintDefaults()
	fmt.Fprint(w, "\nRun 'thronectl help <command>' for the arguments of a command.\n")
}

func (c *cli) globalFlags(fs *flag.FlagSet) {
	fs.StringVar(&c.addr, "addr", c.addr, "Throne API `host:port`; $THRONE_ADDR sets the default")
	fs.StringVar(&c.keyFile, "key-file", c.keyFile, "read the key from `file` instead of $THRONE_KEY or $THRONE_KEY_FILE")
	fs.BoolVar(&c.json, "json", c.json, "print the response body as JSON")
	fs.DurationVar(&c.timeout, "timeout", c.timeout, "give up after this `duration`; 0 waits as long as Throne may take")
}

// parse parses a command's flags, which may stand before, between or after its
// arguments, and checks that it got lo to hi arguments; hi < 0 means no limit.
func (c *cli) parse(fs *flag.FlagSet, args []string, lo, hi int) ([]string, error) {
	c.globalFlags(fs)
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, parseError(err)
		}
		rest := fs.Args()
		if n := len(args) - len(rest); n > 0 && args[n-1] == "--" {
			pos = append(pos, rest...)
			break
		}
		if len(rest) == 0 {
			break
		}
		pos = append(pos, rest[0])
		args = rest[1:]
	}
	switch {
	case len(pos) < lo:
		return nil, c.usageErr("missing argument")
	case hi >= 0 && len(pos) > hi:
		return nil, c.usageErr("unexpected argument %q", pos[hi])
	}
	return pos, nil
}

// open returns a client and the context for the command's calls.
func (c *cli) open() (*throne.Client, context.Context, error) {
	key, err := c.key()
	if err != nil {
		return nil, nil, err
	}
	return throne.New(c.addr, key), c.context(), nil
}

// context returns a new context for the command's calls, with the whole --timeout, and
// ends the one before. Commands that ask a question take a new one after the answer.
func (c *cli) context() context.Context {
	if c.cancel != nil {
		c.cancel()
		c.cancel = nil
	}
	ctx := rawbody.With(context.Background(), &c.raw)
	if c.timeout > 0 {
		ctx, c.cancel = context.WithTimeout(ctx, c.timeout)
	}
	return ctx
}

// isTerminal says whether r is a terminal, on which thronectl can ask for confirmation.
var isTerminal = func(r io.Reader) bool {
	f, ok := r.(*os.File)
	if !ok {
		return false
	}
	fi, err := f.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

// canConfirm checks, before any work, that a command that asks first can go on: yes was
// given, or there is a terminal to ask on.
func (c *cli) canConfirm(yes bool, what string) error {
	if !yes && !isTerminal(c.stdin) {
		return c.usageErr("standard input is not a terminal to confirm on: add --yes to %s without asking", what)
	}
	return nil
}

// ask asks a question on the terminal; any answer but y or yes is no.
func (c *cli) ask(question string) bool {
	fmt.Fprintf(c.stderr, "%s [y/N] ", question)
	line, _ := bufio.NewReader(c.stdin).ReadString('\n')
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true
	}
	return false
}

func (c *cli) key() (throne.Key, error) {
	var key throne.Key
	var err error
	switch text, file := c.getenv("THRONE_KEY"), c.getenv("THRONE_KEY_FILE"); {
	case c.keyFile != "":
		key, err = throne.ReadKeyFile(c.keyFile)
	case text != "":
		if key, err = throne.ParseKey(text); err != nil {
			err = errors.New("THRONE_KEY does not hold a valid key: want the 43-character text that Throne shows")
		}
	case file != "":
		key, err = throne.ReadKeyFile(file)
	default:
		err = errors.New("no key: set THRONE_KEY or THRONE_KEY_FILE, or pass --key-file. In Throne, " +
			"Preferences > Integration Settings > Throne API > Copy for scripts copies THRONE_ADDR and THRONE_KEY")
	}
	if err != nil {
		return key, usageError{msg: strings.TrimPrefix(err.Error(), "throne: ")}
	}
	return key, nil
}

// errReported is a usage error that the flag package or the usage text already showed.
var errReported = errors.New("usage error reported")

func parseError(err error) error {
	if errors.Is(err, flag.ErrHelp) {
		return err
	}
	return errReported
}

// usageError is a mistake on the command line or in the configuration: exit status 2.
type usageError struct {
	msg string
	cmd *command
}

func (e usageError) Error() string { return e.msg }

func (c *cli) usageErr(format string, args ...any) error {
	return usageError{msg: fmt.Sprintf(format, args...), cmd: c.cmd}
}

// failure is work that failed in part, which the output shows, or a confirmation that was
// declined: exit status 1.
type failure struct {
	msg string
}

func (e failure) Error() string { return e.msg }

func (c *cli) exit(err error) int {
	var uerr usageError
	var ferr failure
	var apiErr *throne.APIError
	var opErr *net.OpError
	switch {
	case err == nil, errors.Is(err, flag.ErrHelp):
		return 0
	case errors.Is(err, errReported):
		return 2
	case errors.As(err, &uerr):
		if uerr.cmd != nil {
			fmt.Fprintf(c.stderr, "thronectl %s: %s\nUsage: %s\n", uerr.cmd.name, uerr.msg, uerr.cmd.synopsis())
		} else {
			fmt.Fprintf(c.stderr, "thronectl: %s\n", uerr.msg)
		}
		return 2
	case errors.As(err, &ferr):
		fmt.Fprintf(c.stderr, "error: %s\n", ferr.msg)
		return 1
	case errors.As(err, &apiErr):
		fmt.Fprintf(c.stderr, "error: %s: %s\n", clean(apiErr.Code), clean(apiErr.Message))
		return 1
	case errors.Is(err, throne.ErrRejected):
		fmt.Fprintln(c.stderr, "error: Throne closed the connection without a response: the key is probably wrong. "+
			"Copy it again in Throne: Preferences > Integration Settings > Throne API > Copy for scripts. "+
			"From another device, check that its address is allowed there.")
	case errors.As(err, &opErr) && opErr.Op == "dial":
		fmt.Fprintf(c.stderr, "error: %s\nIs Throne running, with the Throne API enabled for %s?\n",
			strings.TrimPrefix(err.Error(), "throne: "), c.addr)
	default:
		fmt.Fprintf(c.stderr, "error: %s\n", strings.TrimPrefix(err.Error(), "throne: "))
	}
	return 3
}

// optInt is an integer flag that tells whether it was given; 0 is a valid ID.
type optInt struct {
	v   int
	set bool
}

func (o *optInt) String() string {
	if o == nil || !o.set {
		return ""
	}
	return strconv.Itoa(o.v)
}

func (o *optInt) Set(s string) error {
	v, err := strconv.Atoi(s)
	if err != nil {
		return errors.New("want an integer")
	}
	o.v, o.set = v, true
	return nil
}

// optDuration is a duration flag that also takes days, such as 7d, and tells whether
// it was given.
type optDuration struct {
	d   time.Duration
	set bool
}

func (o *optDuration) String() string {
	if o == nil || !o.set {
		return ""
	}
	return o.d.String()
}

func (o *optDuration) Set(s string) error {
	d, err := time.ParseDuration(s)
	if days, ok := strings.CutSuffix(s, "d"); ok {
		var n float64
		n, err = strconv.ParseFloat(days, 64)
		d = time.Duration(n * float64(24*time.Hour))
	}
	if err != nil || d < 0 {
		return errors.New("want a duration such as 90m, 24h or 7d")
	}
	o.d, o.set = d, true
	return nil
}

// idList is a flag of comma-separated IDs.
type idList []int

func (l *idList) String() string {
	if l == nil {
		return ""
	}
	return joinInts(*l)
}

func (l *idList) Set(s string) error {
	ids, err := parseIDs([]string{s})
	if err != nil {
		return err
	}
	*l = append(*l, ids...)
	return nil
}

// parseIDs parses profile IDs, each argument holding one or more separated by commas.
func parseIDs(args []string) ([]int, error) {
	var ids []int
	for _, arg := range args {
		for part := range strings.SplitSeq(arg, ",") {
			id, err := strconv.Atoi(strings.TrimSpace(part))
			if err != nil {
				return nil, fmt.Errorf("not an ID: %q", part)
			}
			ids = append(ids, id)
		}
	}
	return ids, nil
}

func joinInts(ids []int) string {
	s := make([]string, len(ids))
	for i, id := range ids {
		s[i] = strconv.Itoa(id)
	}
	return strings.Join(s, ",")
}

func versionString() string {
	if version == "dev" {
		if bi, ok := debug.ReadBuildInfo(); ok && bi.Main.Version != "" && bi.Main.Version != "(devel)" {
			return bi.Main.Version
		}
	}
	return version
}
