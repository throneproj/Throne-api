package main

import (
	"context"
	"embed"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"

	throne "github.com/throneproj/Throne-api"
)

// The scripts that 'thronectl completion SHELL' prints run
//
//	thronectl __complete WORD... CURRENT
//
// with the words after the program name, CURRENT being the word under the cursor up to the
// cursor, "" after a space. thronectl prints a candidate per line, followed by a tab and a
// description where it has one, then a last line ":N", N the sum of these directives:
//
//   - 1: no space after the candidate;
//   - 2: file names complete CURRENT too, or the text after the first "=" of a CURRENT
//     such as --key-file=FILE.
//
// It prints nothing else and exits 0, whatever the words.
const completeCommand = "__complete"

const (
	directiveNoSpace = 1 << iota
	directiveFiles
)

// completionWait bounds the requests to Throne for one completion.
var completionWait = time.Second

// completionShells are the shells completion prints a script for, and the script's file.
var completionShells = []struct{ name, file string }{
	{"bash", "thronectl.bash"},
	{"zsh", "thronectl.zsh"},
	{"fish", "thronectl.fish"},
	{"powershell", "thronectl.ps1"},
}

//go:embed completions/*
var completionScripts embed.FS

func cmdCompletion(c *cli, fs *flag.FlagSet, args []string) error {
	pos, err := c.parse(fs, args, 1, 1)
	if err != nil {
		return err
	}
	var names []string
	for _, sh := range completionShells {
		if sh.name == pos[0] {
			script, err := completionScripts.ReadFile("completions/" + sh.file)
			if err != nil {
				return err
			}
			_, err = c.stdout.Write(script)
			return err
		}
		names = append(names, sh.name)
	}
	return c.usageErr("want %s or %s, not %q", strings.Join(names[:len(names)-1], ", "), names[len(names)-1], pos[0])
}

// candidate is a completion and its description, which zsh, fish and PowerShell show.
type candidate struct {
	text, desc string
}

type completion struct {
	cands     []candidate
	directive int
}

// values completes a word. complete keeps the candidates that start with the word.
type values func(x *completer, word string) completion

// completer completes one command line.
type completer struct {
	c      *cli
	ctx    context.Context
	seen   map[string]bool // the flags on the line
	client *throne.Client
	opened bool
}

// api returns a client for Throne when thronectl finds a key as the other commands do,
// or else nil.
func (x *completer) api() *throne.Client {
	if !x.opened {
		x.opened = true
		// A key file on a share that is gone may not open for long: the deadline covers it.
		keys, find := make(chan throne.Key, 1), completionKey
		go func() {
			if key, err := find(x.c); err == nil {
				keys <- key
			}
			close(keys)
		}()
		select {
		case key, ok := <-keys:
			if ok {
				x.client = throne.New(x.c.addr, key)
			}
		case <-x.ctx.Done():
		}
	}
	return x.client
}

// completionKey finds the key as the other commands do, but reads only a regular file: a
// pipe or a device such as /dev/stdin would read the terminal the shell completes on.
var completionKey = func(c *cli) (throne.Key, error) {
	file := c.keyFile
	if file == "" && c.getenv("THRONE_KEY") == "" {
		file = c.getenv("THRONE_KEY_FILE")
	}
	if file != "" {
		if fi, err := os.Stat(file); err != nil || !fi.Mode().IsRegular() {
			return throne.Key{}, errors.New("no regular key file")
		}
	}
	return c.key()
}

func (x *completer) run(v values, word string) completion {
	if v == nil {
		return completion{}
	}
	return v(x, word)
}

// set takes the flags that tell where Throne is from the command line.
func (x *completer) set(name, value string) {
	switch name {
	case "addr":
		x.c.addr = value
	case "key-file":
		x.c.keyFile = expandHome(value)
	}
}

// expandHome expands a leading ~, which the shell leaves alone in a word being completed.
func expandHome(name string) string {
	rest, ok := strings.CutPrefix(name, "~")
	if !ok || rest != "" && rest[0] != '/' && rest[0] != filepath.Separator {
		return name
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return name
	}
	return home + rest
}

// completeWords prints the completion of words for the shell scripts.
func (c *cli) completeWords(words []string) {
	var res completion
	func() {
		defer func() { recover() }()
		res = c.complete(words)
	}()
	var b strings.Builder
	for _, cd := range res.cands {
		b.WriteString(clean(cd.text))
		if cd.desc != "" {
			b.WriteString("\t" + clean(cd.desc))
		}
		b.WriteByte('\n')
	}
	fmt.Fprintf(&b, ":%d\n", res.directive)
	io.WriteString(c.stdout, b.String())
}

// complete completes the last of words, which follow the program name.
func (c *cli) complete(words []string) completion {
	x := &completer{c: c, seen: map[string]bool{}}
	var cancel context.CancelFunc
	x.ctx, cancel = context.WithTimeout(context.Background(), completionWait)
	defer cancel()

	if len(words) == 0 {
		words = []string{""}
	}
	word, before := words[len(words)-1], words[:len(words)-1]
	var (
		cmd    *command
		help   bool // help takes the words after it as they are
		args   []string
		fs     = globalFlagSet()
		value  *flag.Flag // the flag that word is the value of
		dashes bool       // after --
	)
	for i := 0; i < len(before); i++ {
		w := before[i]
		switch {
		case !dashes && !help && w == "--":
			dashes = true
		case !dashes && !help && len(w) > 1 && w[0] == '-':
			name, v, hasValue := strings.Cut(strings.TrimLeft(w, "-"), "=")
			x.seen[name] = true
			f := fs.Lookup(name)
			if f == nil || isBoolFlag(f) {
				continue
			}
			if !hasValue {
				if i == len(before)-1 {
					value = f
					continue
				}
				i++
				v = before[i]
			}
			x.set(name, v)
		case cmd == nil && !help:
			if w == "help" {
				help = true
				continue
			}
			if cmd = findCommand(w); cmd == nil {
				return completion{}
			}
			fs, dashes = commandFlags(cmd), false
		default:
			args = append(args, w)
		}
	}

	var res completion
	flagWord := !dashes && !help && strings.HasPrefix(word, "-")
	switch {
	case value != nil:
		res = x.run(flagValues[value.Name], word)
	case flagWord && strings.Contains(word, "="):
		name, v, _ := strings.Cut(word, "=")
		if f := fs.Lookup(strings.TrimLeft(name, "-")); f != nil && !isBoolFlag(f) {
			res = x.run(flagValues[f.Name], v)
			for i := range res.cands {
				res.cands[i].text = name + "=" + res.cands[i].text
			}
		}
	case flagWord:
		res.cands = x.flagCandidates(fs, cmd, args)
		if word == "-" {
			pos := x.positional(fs, cmd, help, args, word)
			res.cands = append(res.cands, pos.cands...)
			res.directive = pos.directive
		}
	default:
		res = x.positional(fs, cmd, help, args, word)
	}
	res.cands = keep(res.cands, func(cd candidate) bool { return strings.HasPrefix(cd.text, word) })
	return res
}

// positional completes the argument after args, given the flags of cmd in fs.
func (x *completer) positional(fs *flag.FlagSet, cmd *command, help bool, args []string, word string) completion {
	if cmd == nil {
		if len(args) > 0 {
			return completion{}
		}
		return x.run(commandNames(!help), word)
	}
	next := argValues[cmd.name]
	alt := alternatives[cmd.name]
	if next == nil || len(args) >= alt.from && slices.ContainsFunc(alt.flags, func(name string) bool { return x.seen[name] }) {
		return completion{}
	}
	// A flag such as --all of sub, for "update: ...", leaves only that subcommand.
	var only []string
	fs.VisitAll(func(f *flag.Flag) {
		if s := scope(f); s != "" && x.seen[f.Name] && !slices.Contains(only, s) {
			only = append(only, s)
		}
	})
	var given []string
	for _, a := range args {
		for item := range strings.SplitSeq(a, ",") {
			given = append(given, listItem(item))
		}
	}
	res := x.run(next(args), word)
	res.cands = keep(res.cands, func(cd candidate) bool {
		if len(args) == 0 && slices.ContainsFunc(only, func(s string) bool { return s != cd.text }) {
			return false
		}
		return !slices.Contains(given, listItem(cd.text[strings.LastIndex(cd.text, ",")+1:]))
	})
	return res
}

// alternative names flags that stand for the arguments from index from on, such as test's
// --group for profile IDs: completion offers the flags or the arguments, not both.
type alternative struct {
	from  int
	flags []string
}

var alternatives = map[string]alternative{
	"test":    {1, []string{"group", "name"}},
	"clear":   {0, []string{"group", "name"}},
	"profile": {1, []string{"group", "name"}},
	"sub":     {1, []string{"all"}},
	"import":  {0, []string{"url"}},
}

// scope returns the subcommand or keyword that a flag goes with, which its usage starts
// with, such as add for "add: the group's name", or "".
func scope(f *flag.Flag) string {
	k, _, ok := strings.Cut(f.Usage, ": ")
	if !ok || k == "" || strings.Trim(k, "abcdefghijklmnopqrstuvwxyz") != "" {
		return ""
	}
	return k
}

// keep returns the candidates that ok keeps, in a new slice.
func keep(cands []candidate, ok func(candidate) bool) []candidate {
	var out []candidate
	for _, cd := range cands {
		if ok(cd) {
			out = append(out, cd)
		}
	}
	return out
}

func findCommand(name string) *command {
	for _, cmd := range commands {
		if cmd.name == name {
			return cmd
		}
	}
	return nil
}

func globalFlagSet() *flag.FlagSet {
	fs := flag.NewFlagSet("thronectl", flag.ContinueOnError)
	new(cli).globalFlags(fs)
	return fs
}

// commandFlags returns the flags of cmd, the global ones included. Every command defines
// its flags before it parses its arguments, so running it with -h on a scratch cli
// defines them and does nothing else.
func commandFlags(cmd *command) *flag.FlagSet {
	c := &cli{stdin: strings.NewReader(""), stdout: io.Discard, stderr: io.Discard, getenv: func(string) string { return "" }}
	fs := flag.NewFlagSet("thronectl "+cmd.name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.Usage = func() {}
	cmd.run(c, fs, []string{"-h"})
	return fs
}

func isBoolFlag(f *flag.Flag) bool {
	b, ok := f.Value.(interface{ IsBoolFlag() bool })
	return ok && b.IsBoolFlag()
}

// flagText is a flag as the help shows it: -o, but --name.
func flagText(name string) string {
	if len(name) == 1 {
		return "-" + name
	}
	return "--" + name
}

// flagCandidates lists the flags of fs that may come next: not those given already, unless
// their values add up as those of --ids do, those that go with another subcommand or
// keyword than the one given, or those that stand for the arguments given.
func (x *completer) flagCandidates(fs *flag.FlagSet, cmd *command, args []string) []candidate {
	var alt alternative
	if cmd != nil {
		alt = alternatives[cmd.name]
	}
	var cands []candidate
	fs.VisitAll(func(f *flag.Flag) {
		_, adds := f.Value.(*idList)
		switch s := scope(f); {
		case x.seen[f.Name] && !adds:
		case len(args) > 0 && s != "" && s != args[0]:
		case len(args) > alt.from && slices.Contains(alt.flags, f.Name):
		default:
			_, usage := flag.UnquoteUsage(f)
			cands = append(cands, candidate{flagText(f.Name), usage})
		}
	})
	return cands
}

func commandNames(withHelp bool) values {
	return func(*completer, string) completion {
		var res completion
		for _, cmd := range commands {
			res.cands = append(res.cands, candidate{cmd.name, cmd.summary()})
		}
		if withHelp {
			res.cands = append(res.cands, candidate{"help", "Describe a command"})
		}
		return res
	}
}

// flagValues completes the values of flags, by name; nil marks text to type. Every flag
// that takes a value is here, which TestCompletionCoversFlags checks.
var flagValues = map[string]values{
	"addr":     nil,
	"key-file": fileNames,
	"timeout":  nil,
	"group":    groupIDs,
	"name":     nil,
	"ids":      commaList(profileIDs),
	"max-age":  nil,
	"by":       words(throne.TrafficByProfile, throne.TrafficByApp),
	"since":    nil,
	"url":      nil,
	"o":        fileNames,
	"parts":    commaList(backupPartNames),
	"body":     nil,
}

// argValues completes the arguments of each command: given those before the argument under
// the cursor, it returns what completes that one. A nil function or values means nothing
// to complete. Every command is here, which TestCompletionCoversCommands checks.
var argValues = map[string]func(args []string) values{
	"status":   nil,
	"groups":   nil,
	"profiles": nil,
	"connect": first(either(fixed(
		candidate{"last", "the profile that ran last"},
		candidate{"best", "the profile with the lowest latency"}), profileIDs)),
	"disconnect": nil,
	"toggle":     nil,
	"tun":        first(words("on", "off", "toggle")),
	"proxy":      first(words("on", "off", "toggle")),
	"routes":     nil,
	"route":      first(routeIDs),
	"test": func(args []string) values {
		if len(args) == 0 {
			return words(string(throne.TestLatency), string(throne.TestIP), string(throne.TestSpeed))
		}
		return commaList(profileIDs)
	},
	"jobs":    nil,
	"job":     first(jobIDs),
	"cancel":  first(runningJobIDs),
	"clear":   each(commaList(profileIDs)),
	"live":    nil,
	"traffic": nil,
	"import":  first(either(fixed(candidate{"-", "standard input"}), fileNames)),
	"sub": subcommands(
		sub{"add", "Add a subscription group", nil},
		sub{"update", "Update subscription groups", each(commaList(subscriptionIDs))}),
	"group": subcommands(
		sub{"add", "Add a group", nil},
		sub{"rm", "Delete a group with its profiles", first(groupIDs)}),
	"profile": subcommands(sub{"rm", "Delete profiles", each(commaList(profileIDs))}),
	"settings": func(args []string) values {
		switch {
		case len(args) == 0:
			return either(fixed(candidate{"get", "Show settings"}, candidate{"set", "Change settings"}), settingValues(false, nil))
		case args[0] == "set":
			return settingValues(true, args[1:])
		}
		return settingValues(false, args)
	},
	"backup": subcommands(
		sub{"create", "Back up to a file", nil},
		sub{"restore", "Restore from a file", first(fileNames)}),
	"rulesets": subcommands(sub{"update", "Update the remote rule-sets", nil}),
	"geo": subcommands(
		sub{"status", "Show the geo files", nil},
		sub{"update", "Download the geo files", each(words(geoFiles...))}),
	"restart":  nil,
	"shutdown": nil,
	"keygen":   nil,
	"call": func(args []string) values {
		switch len(args) {
		case 0:
			return callMethods
		case 1:
			return callPaths(strings.ToUpper(args[0]))
		}
		return nil
	},
	"completion": first(func(*completer, string) completion {
		var res completion
		for _, sh := range completionShells {
			res.cands = append(res.cands, candidate{sh.name, ""})
		}
		return res
	}),
	"version": nil,
}

// first completes the first argument with v, and nothing after it.
func first(v values) func([]string) values {
	return func(args []string) values {
		if len(args) == 0 {
			return v
		}
		return nil
	}
}

// each completes every argument with v.
func each(v values) func([]string) values {
	return func([]string) values { return v }
}

// sub is a subcommand, and what completes its arguments.
type sub struct {
	name, desc string
	args       func([]string) values
}

func subcommands(subs ...sub) func([]string) values {
	return func(args []string) values {
		if len(args) == 0 {
			var cands []candidate
			for _, s := range subs {
				cands = append(cands, candidate{s.name, s.desc})
			}
			return fixed(cands...)
		}
		for _, s := range subs {
			if s.name == args[0] && s.args != nil {
				return s.args(args[1:])
			}
		}
		return nil
	}
}

func fixed(cands ...candidate) values {
	return func(*completer, string) completion { return completion{cands: slices.Clone(cands)} }
}

func words(texts ...string) values {
	cands := make([]candidate, len(texts))
	for i, t := range texts {
		cands[i].text = t
	}
	return fixed(cands...)
}

func either(vs ...values) values {
	return func(x *completer, word string) completion {
		var res completion
		for _, v := range vs {
			r := v(x, word)
			res.cands = append(res.cands, r.cands...)
			res.directive |= r.directive
		}
		return res
	}
}

func fileNames(*completer, string) completion {
	return completion{directive: directiveFiles}
}

// commaList completes the last item of a comma-separated list with v, leaving out the
// items before it.
func commaList(v values) values {
	return func(x *completer, word string) completion {
		i := strings.LastIndex(word, ",") + 1
		head := word[:i]
		var given []string
		for item := range strings.SplitSeq(head, ",") {
			given = append(given, listItem(item))
		}
		res := v(x, word[i:])
		res.cands = keep(res.cands, func(cd candidate) bool { return !slices.Contains(given, listItem(cd.text)) })
		for i := range res.cands {
			res.cands[i].text = head + res.cands[i].text
		}
		return res
	}
}

// listItem spells an item of a list as parseParts does, so that ip-lists is ip_lists.
func listItem(s string) string {
	return strings.ReplaceAll(strings.ToLower(strings.TrimSpace(s)), "-", "_")
}

var partDescs = map[string]string{
	throne.PartProfiles: "groups and profiles",
	throne.PartRoutes:   "routing profiles and their rules",
	throne.PartSettings: "settings",
	throne.PartOTP:      "OTP accounts, secrets included",
	throne.PartIPLists:  "IP lists and IP scans",
	throne.PartIcons:    "icon files",
}

func backupPartNames(_ *completer, word string) completion {
	var res completion
	for _, p := range backupParts {
		res.cands = append(res.cands, candidate{p, partDescs[p]})
	}
	if strings.HasPrefix(word, "ip-") {
		res.cands = append(res.cands, candidate{"ip-lists", partDescs[throne.PartIPLists]})
	}
	return res
}

// apiPaths are the paths of spec/api.md without an ID in them, by method, for call.
var apiPaths = map[string][]string{
	"GET": {"/v1/status", "/v1/groups", "/v1/profiles", "/v1/routes", "/v1/tests", "/v1/stats/live",
		"/v1/stats/traffic", "/v1/settings", "/v1/geo-assets"},
	"POST": {"/v1/import", "/v1/groups", "/v1/groups/update", "/v1/tests", "/v1/rule-sets/update",
		"/v1/geo-assets/update", "/v1/backups", "/v1/restores", "/v1/restart", "/v1/shutdown"},
	"PUT":    {"/v1/connection", "/v1/modes", "/v1/routes/active", "/v1/settings"},
	"DELETE": {"/v1/profiles", "/v1/connection", "/v1/results"},
}

// callMethods completes the methods of call, in lower case once the word is, which call
// takes too.
func callMethods(_ *completer, word string) completion {
	var res completion
	for _, m := range []string{"GET", "POST", "PUT", "DELETE"} {
		if word != "" && word == strings.ToLower(word) {
			m = strings.ToLower(m)
		}
		res.cands = append(res.cands, candidate{m, ""})
	}
	return res
}

// callPaths completes the paths of method, without the leading slash once the word lacks
// it, as call takes them, and on Windows for an empty word too: Git Bash turns /v1/status
// into a path under its own directory.
func callPaths(method string) values {
	return func(_ *completer, word string) completion {
		var res completion
		for _, p := range apiPaths[method] {
			if word != "" && word[0] != '/' || word == "" && runtime.GOOS == "windows" {
				p = p[1:]
			}
			res.cands = append(res.cands, candidate{p, ""})
		}
		return res
	}
}

// settingNames are the settings of spec/api.md, true for those that take true or false.
// Completion offers them when Throne does not answer.
var settingNames = map[string]bool{
	"remember_enable":             true,
	"log_level":                   false,
	"xray_log_level":              false,
	"inbound_enabled":             true,
	"inbound_address":             false,
	"inbound_port":                false,
	"test_url":                    false,
	"test_concurrency":            false,
	"test_timeout_ms":             false,
	"sub_auto_update":             false,
	"sub_respect_server_interval": true,
	"route_auto_update":           false,
	"kill_switch":                 true,
	"tun_strict_route":            true,
	"tun_auto_redirect":           true,
	"tun_mtu":                     false,
	"tun_ipv6":                    true,
	"remote_dns":                  false,
	"direct_dns":                  false,
	"ntp_enabled":                 true,
	"ntp_server":                  false,
	"ntp_interval":                false,
}

// settingValues completes setting names, those of Throne when it answers, leaving out
// those given; with assign, as NAME= for settings set, or the values of a bool setting
// after the =.
func settingValues(assign bool, given []string) values {
	return func(x *completer, word string) completion {
		var live map[string]any
		if cl := x.api(); cl != nil {
			live, _ = cl.Settings(x.ctx)
		}
		isBool := func(name string) bool {
			if live != nil {
				_, ok := live[name].(bool)
				return ok
			}
			return settingNames[name]
		}
		if name, _, ok := strings.Cut(word, "="); ok && assign {
			if !isBool(name) {
				return completion{}
			}
			return completion{cands: []candidate{{name + "=true", ""}, {name + "=false", ""}}}
		}
		var names []string
		if live != nil {
			for name := range live {
				names = append(names, name)
			}
		} else {
			for name := range settingNames {
				names = append(names, name)
			}
		}
		slices.Sort(names)
		var res completion
		for _, name := range names {
			if slices.ContainsFunc(given, func(g string) bool { return g == name || strings.HasPrefix(g, name+"=") }) {
				continue
			}
			cd := candidate{text: name}
			if live != nil {
				cd.desc = settingText(live[name])
			}
			if assign {
				cd.text += "="
			}
			res.cands = append(res.cands, cd)
		}
		if assign {
			res.directive = directiveNoSpace
		}
		return res
	}
}

// fromThrone completes IDs with what Throne lists, keeping the items that cand accepts.
// Without a key, or when Throne does not answer in time, there are none.
func fromThrone[T any](list func(*throne.Client, context.Context) ([]T, error), cand func(T) (candidate, bool)) values {
	return func(x *completer, word string) completion {
		if strings.Trim(word, "0123456789") != "" {
			return completion{}
		}
		cl := x.api()
		if cl == nil {
			return completion{}
		}
		items, err := list(cl, x.ctx)
		if err != nil {
			return completion{}
		}
		var res completion
		for _, it := range items {
			if cd, ok := cand(it); ok {
				res.cands = append(res.cands, cd)
			}
		}
		return res
	}
}

var (
	profileIDs = fromThrone(func(cl *throne.Client, ctx context.Context) ([]throne.Profile, error) {
		return cl.Profiles(ctx, throne.Selection{})
	}, func(p throne.Profile) (candidate, bool) {
		return candidate{strconv.Itoa(p.ID), p.Name}, true
	})
	groupIDs = fromThrone((*throne.Client).Groups, func(g throne.Group) (candidate, bool) {
		return candidate{strconv.Itoa(g.ID), g.Name}, true
	})
	subscriptionIDs = fromThrone((*throne.Client).Groups, func(g throne.Group) (candidate, bool) {
		return candidate{strconv.Itoa(g.ID), g.Name}, g.Subscription
	})
	routeIDs = fromThrone((*throne.Client).Routes, func(r throne.Route) (candidate, bool) {
		if r.Active {
			return candidate{strconv.Itoa(r.ID), r.Name + " (active)"}, true
		}
		return candidate{strconv.Itoa(r.ID), r.Name}, true
	})
	jobIDs = fromThrone((*throne.Client).Jobs, func(j throne.Job) (candidate, bool) {
		return candidate{strconv.Itoa(j.ID), fmt.Sprintf("%s test, %s", j.Kind, j.State)}, true
	})
	runningJobIDs = fromThrone((*throne.Client).Jobs, func(j throne.Job) (candidate, bool) {
		return candidate{strconv.Itoa(j.ID), fmt.Sprintf("%s test", j.Kind)}, j.State == throne.JobRunning
	})
)
