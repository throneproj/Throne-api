package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	throne "github.com/throneproj/Throne-api"
)

func cmdStatus(c *cli, fs *flag.FlagSet, args []string) error {
	if _, err := c.parse(fs, args, 0, 0); err != nil {
		return err
	}
	cl, ctx, err := c.open()
	if err != nil {
		return err
	}
	st, err := cl.Status(ctx)
	if err != nil {
		return err
	}
	return c.show(func(w io.Writer) { writeStatus(w, st) })
}

func cmdGroups(c *cli, fs *flag.FlagSet, args []string) error {
	if _, err := c.parse(fs, args, 0, 0); err != nil {
		return err
	}
	cl, ctx, err := c.open()
	if err != nil {
		return err
	}
	groups, err := cl.Groups(ctx)
	if err != nil {
		return err
	}
	return c.show(func(w io.Writer) { writeGroups(w, groups) })
}

func cmdProfiles(c *cli, fs *flag.FlagSet, args []string) error {
	var group optInt
	var ids idList
	fs.Var(&group, "group", "only the profiles of this group `ID`")
	name := fs.String("name", "", "only the profiles whose names match this case-insensitive `regex`")
	fs.Var(&ids, "ids", "only these profiles, comma-separated `IDs`")
	if _, err := c.parse(fs, args, 0, 0); err != nil {
		return err
	}
	sel := throne.Selection{IDs: ids, Name: *name}
	if group.set {
		sel.Group = &group.v
	}
	cl, ctx, err := c.open()
	if err != nil {
		return err
	}
	profiles, err := cl.Profiles(ctx, sel)
	if err != nil {
		return err
	}
	return c.show(func(w io.Writer) { writeProfiles(w, profiles) })
}

func cmdConnect(c *cli, fs *flag.FlagSet, args []string) error {
	var group optInt
	var maxAge optDuration
	fs.Var(&group, "group", "best: pick from this group `ID` (default: the group Throne shows)")
	name := fs.String("name", "", "best: pick from the profiles whose names match this `regex`")
	fs.Var(&maxAge, "max-age", "best: retest profiles whose latency result is older than this `duration` (default 1h; 0 retests all)")
	restart := fs.Bool("restart", false, "restart the profile if it already runs")
	noWait := fs.Bool("no-wait", false, "return at once instead of waiting for the profile to start")
	pos, err := c.parse(fs, args, 1, 1)
	if err != nil {
		return err
	}
	target := pos[0]
	if target != "best" && (group.set || *name != "" || maxAge.set) {
		return c.usageErr("--group, --name and --max-age go with connect best")
	}
	var id int
	if target != "last" && target != "best" {
		if id, err = strconv.Atoi(target); err != nil {
			return c.usageErr("want a profile ID, last or best, not %q", target)
		}
	}
	opts := waitOptions(*noWait)
	if *restart {
		opts = append(opts, throne.Restart())
	}
	cl, ctx, err := c.open()
	if err != nil {
		return err
	}
	var st *throne.Status
	switch target {
	case "last":
		st, err = cl.ConnectLast(ctx, opts...)
	case "best":
		sel := throne.Selection{Name: *name}
		if group.set {
			sel.Group = &group.v
		}
		if maxAge.set {
			opts = append(opts, throne.MaxAge(maxAge.d))
		}
		st, err = cl.ConnectLowestLatency(ctx, sel, opts...)
	default:
		st, err = cl.Connect(ctx, id, opts...)
	}
	if err != nil {
		return err
	}
	return c.show(func(w io.Writer) { writeStatus(w, st) })
}

func cmdDisconnect(c *cli, fs *flag.FlagSet, args []string) error {
	noWait := fs.Bool("no-wait", false, "return at once instead of waiting for the profile to stop")
	if _, err := c.parse(fs, args, 0, 0); err != nil {
		return err
	}
	cl, ctx, err := c.open()
	if err != nil {
		return err
	}
	st, err := cl.Disconnect(ctx, waitOptions(*noWait)...)
	if err != nil {
		return err
	}
	return c.show(func(w io.Writer) { writeStatus(w, st) })
}

func cmdToggle(c *cli, fs *flag.FlagSet, args []string) error {
	noWait := fs.Bool("no-wait", false, "return at once instead of waiting for the change")
	if _, err := c.parse(fs, args, 0, 0); err != nil {
		return err
	}
	cl, ctx, err := c.open()
	if err != nil {
		return err
	}
	st, err := cl.Status(ctx)
	if err != nil {
		return err
	}
	if st.State == throne.StateIdle || st.State == throne.StateStopping {
		st, err = cl.ConnectLast(ctx, waitOptions(*noWait)...)
	} else {
		st, err = cl.Disconnect(ctx, waitOptions(*noWait)...)
	}
	if err != nil {
		return err
	}
	return c.show(func(w io.Writer) { writeStatus(w, st) })
}

func cmdTun(c *cli, fs *flag.FlagSet, args []string) error {
	return switchMode(c, fs, args,
		func(m *throne.Modes, on *bool) { m.Tun = on },
		func(st *throne.Status) bool { return st.Tun })
}

func cmdProxy(c *cli, fs *flag.FlagSet, args []string) error {
	return switchMode(c, fs, args,
		func(m *throne.Modes, on *bool) { m.SystemProxy = on },
		func(st *throne.Status) bool { return st.SystemProxy })
}

func switchMode(c *cli, fs *flag.FlagSet, args []string, set func(*throne.Modes, *bool), current func(*throne.Status) bool) error {
	noWait := fs.Bool("no-wait", false, "return at once instead of waiting for a restart")
	pos, err := c.parse(fs, args, 1, 1)
	if err != nil {
		return err
	}
	on := pos[0] == "on"
	if pos[0] != "on" && pos[0] != "off" && pos[0] != "toggle" {
		return c.usageErr("want on, off or toggle, not %q", pos[0])
	}
	cl, ctx, err := c.open()
	if err != nil {
		return err
	}
	if pos[0] == "toggle" {
		st, err := cl.Status(ctx)
		if err != nil {
			return err
		}
		on = !current(st)
	}
	var m throne.Modes
	set(&m, &on)
	st, err := cl.SetModes(ctx, m, waitOptions(*noWait)...)
	if err != nil {
		return err
	}
	return c.show(func(w io.Writer) { writeStatus(w, st) })
}

func cmdRoutes(c *cli, fs *flag.FlagSet, args []string) error {
	if _, err := c.parse(fs, args, 0, 0); err != nil {
		return err
	}
	cl, ctx, err := c.open()
	if err != nil {
		return err
	}
	routes, err := cl.Routes(ctx)
	if err != nil {
		return err
	}
	return c.show(func(w io.Writer) { writeRoutes(w, routes) })
}

func cmdRoute(c *cli, fs *flag.FlagSet, args []string) error {
	noWait := fs.Bool("no-wait", false, "return at once instead of waiting for a restart")
	pos, err := c.parse(fs, args, 1, 1)
	if err != nil {
		return err
	}
	id, err := c.id(pos[0])
	if err != nil {
		return err
	}
	cl, ctx, err := c.open()
	if err != nil {
		return err
	}
	st, err := cl.SetRoute(ctx, id, waitOptions(*noWait)...)
	if err != nil {
		return err
	}
	return c.show(func(w io.Writer) { writeStatus(w, st) })
}

func cmdTest(c *cli, fs *flag.FlagSet, args []string) error {
	var group optInt
	fs.Var(&group, "group", "test the profiles of this group `ID`")
	name := fs.String("name", "", "with --group: only the profiles whose names match this `regex`")
	noWait := fs.Bool("no-wait", false, "start the test and return at once")
	pos, err := c.parse(fs, args, 1, -1)
	if err != nil {
		return err
	}
	kind := throne.TestKind(pos[0])
	if kind != throne.TestLatency && kind != throne.TestIP && kind != throne.TestSpeed {
		return c.usageErr("want latency, ip or speed, not %q", pos[0])
	}
	sel, err := c.selection(group, *name, pos[1:])
	if err != nil {
		return err
	}
	cl, ctx, err := c.open()
	if err != nil {
		return err
	}
	var job *throne.Job
	if *noWait {
		job, err = cl.StartTest(ctx, kind, sel)
	} else {
		job, err = cl.RunTest(ctx, kind, sel)
	}
	if err != nil {
		if job != nil {
			return fmt.Errorf("%w\nTest %d is still running in Throne; see it with: thronectl job %d", err, job.ID, job.ID)
		}
		return err
	}
	return c.show(func(w io.Writer) { writeJob(w, job) })
}

func cmdJobs(c *cli, fs *flag.FlagSet, args []string) error {
	if _, err := c.parse(fs, args, 0, 0); err != nil {
		return err
	}
	cl, ctx, err := c.open()
	if err != nil {
		return err
	}
	jobs, err := cl.Jobs(ctx)
	if err != nil {
		return err
	}
	return c.show(func(w io.Writer) { writeJobs(w, jobs) })
}

func cmdJob(c *cli, fs *flag.FlagSet, args []string) error {
	return jobCommand(c, fs, args, (*throne.Client).Job)
}

func cmdCancel(c *cli, fs *flag.FlagSet, args []string) error {
	return jobCommand(c, fs, args, (*throne.Client).CancelJob)
}

func jobCommand(c *cli, fs *flag.FlagSet, args []string, do func(*throne.Client, context.Context, int) (*throne.Job, error)) error {
	pos, err := c.parse(fs, args, 1, 1)
	if err != nil {
		return err
	}
	id, err := c.id(pos[0])
	if err != nil {
		return err
	}
	cl, ctx, err := c.open()
	if err != nil {
		return err
	}
	job, err := do(cl, ctx, id)
	if err != nil {
		return err
	}
	return c.show(func(w io.Writer) { writeJob(w, job) })
}

func cmdClear(c *cli, fs *flag.FlagSet, args []string) error {
	var group optInt
	fs.Var(&group, "group", "clear the results of this group `ID`")
	name := fs.String("name", "", "with --group: only the profiles whose names match this `regex`")
	pos, err := c.parse(fs, args, 0, -1)
	if err != nil {
		return err
	}
	sel, err := c.selection(group, *name, pos)
	if err != nil {
		return err
	}
	cl, ctx, err := c.open()
	if err != nil {
		return err
	}
	n, err := cl.ClearResults(ctx, sel)
	if err != nil {
		return err
	}
	return c.show(func(w io.Writer) { fmt.Fprintf(w, "Cleared the results of %d profiles.\n", n) })
}

func cmdLive(c *cli, fs *flag.FlagSet, args []string) error {
	if _, err := c.parse(fs, args, 0, 0); err != nil {
		return err
	}
	cl, ctx, err := c.open()
	if err != nil {
		return err
	}
	live, err := cl.LiveStats(ctx)
	if err != nil {
		return err
	}
	return c.show(func(w io.Writer) { writeLive(w, live) })
}

func cmdTraffic(c *cli, fs *flag.FlagSet, args []string) error {
	var since optDuration
	by := fs.String("by", throne.TrafficByProfile, "total by profile or app")
	fs.Var(&since, "since", "total over this `duration` before now, such as 7d (default 24h)")
	if _, err := c.parse(fs, args, 0, 0); err != nil {
		return err
	}
	if *by != throne.TrafficByProfile && *by != throne.TrafficByApp {
		return c.usageErr("--by takes profile or app, not %q", *by)
	}
	q := throne.TrafficQuery{By: *by}
	if since.set {
		q.To = time.Now()
		q.From = q.To.Add(-since.d)
	}
	cl, ctx, err := c.open()
	if err != nil {
		return err
	}
	stats, err := cl.Traffic(ctx, q)
	if err != nil {
		return err
	}
	return c.show(func(w io.Writer) { writeTraffic(w, stats) })
}

func cmdCall(c *cli, fs *flag.FlagSet, args []string) error {
	body := fs.String("body", "", "the request body, as `JSON`")
	pos, err := c.parse(fs, args, 2, -1)
	if err != nil {
		return err
	}
	method, path := strings.ToUpper(pos[0]), pos[1]
	// A path without the leading slash dodges Git Bash's path conversion on Windows.
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	var query map[string]string
	for _, kv := range pos[2:] {
		k, v, ok := strings.Cut(kv, "=")
		if !ok || k == "" {
			return c.usageErr("want query parameters as key=value, not %q", kv)
		}
		if query == nil {
			query = map[string]string{}
		}
		query[k] = v
	}
	var b any
	if *body != "" {
		if !json.Valid([]byte(*body)) {
			return c.usageErr("--body is not valid JSON")
		}
		b = json.RawMessage(*body)
	}
	cl, ctx, err := c.open()
	if err != nil {
		return err
	}
	var out json.RawMessage
	if err := cl.Call(ctx, method, path, query, b, &out); err != nil {
		return err
	}
	return writeJSON(c.stdout, out)
}

func cmdVersion(c *cli, fs *flag.FlagSet, args []string) error {
	if _, err := c.parse(fs, args, 0, 0); err != nil {
		return err
	}
	_, err := fmt.Fprintf(c.stdout, "thronectl %s\n", versionString())
	return err
}

func waitOptions(noWait bool) []throne.Option {
	if noWait {
		return []throne.Option{throne.NoWait()}
	}
	return nil
}

func (c *cli) id(s string) (int, error) {
	id, err := strconv.Atoi(s)
	if err != nil {
		return 0, c.usageErr("not an ID: %q", s)
	}
	return id, nil
}

// selection builds the profiles a test or a clear acts on: a group, or IDs.
func (c *cli) selection(group optInt, name string, args []string) (throne.Selection, error) {
	switch {
	case group.set && len(args) > 0:
		return throne.Selection{}, c.usageErr("give --group or profile IDs, not both")
	case group.set:
		return throne.SelectGroup(group.v, name), nil
	case len(args) == 0:
		return throne.Selection{}, c.usageErr("give --group or profile IDs")
	case name != "":
		return throne.Selection{}, c.usageErr("--name goes with --group")
	}
	ids, err := parseIDs(args)
	if err != nil {
		return throne.Selection{}, c.usageErr("%v", err)
	}
	return throne.SelectProfiles(ids...), nil
}
