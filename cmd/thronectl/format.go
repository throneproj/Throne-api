package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"
	"unicode"

	throne "github.com/throneproj/Throne-api"
)

// show prints the response body with --json, or else what human writes.
func (c *cli) show(human func(w io.Writer)) error {
	if c.json {
		return writeJSON(c.stdout, c.raw)
	}
	tw := tabwriter.NewWriter(c.stdout, 0, 0, 2, ' ', 0)
	human(tw)
	return tw.Flush()
}

// writeJSON prints a response body as Throne sent it, indented.
func writeJSON(w io.Writer, raw json.RawMessage) error {
	if len(raw) == 0 {
		raw = json.RawMessage("null")
	}
	var buf bytes.Buffer
	if err := json.Indent(&buf, raw, "", "  "); err != nil {
		buf.Reset()
		buf.Write(raw)
	}
	buf.WriteByte('\n')
	_, err := w.Write(buf.Bytes())
	return err
}

func writeStatus(w io.Writer, st *throne.Status) {
	profile := "none"
	if st.Profile != nil {
		profile = fmt.Sprintf("%d %s (%s)", st.Profile.ID, st.Profile.Name, st.Profile.Type)
	} else if st.LastProfileID >= 0 {
		profile = fmt.Sprintf("none (last: %d)", st.LastProfileID)
	}
	inbound := "off"
	if st.Inbound.Enabled {
		inbound = net.JoinHostPort(st.Inbound.Address, strconv.Itoa(st.Inbound.Port))
	}
	core := "down"
	if st.Core {
		core = "up"
	}
	for _, row := range [][2]string{
		{"State", st.State},
		{"Profile", profile},
		{"Tun", onOff(st.Tun)},
		{"System proxy", onOff(st.SystemProxy)},
		{"Route", fmt.Sprintf("%d %s", st.Route.ID, st.Route.Name)},
		{"Kill switch", st.KillSwitch},
		{"Core", core},
		{"Inbound", inbound},
		{"Version", st.Version},
	} {
		fmt.Fprintf(w, "%s:\t%s\n", row[0], clean(row[1]))
	}
}

func writeGroups(w io.Writer, groups []throne.Group) {
	if len(groups) == 0 {
		fmt.Fprintln(w, "No groups.")
		return
	}
	fmt.Fprintln(w, "ID\tNAME\tPROFILES\tSUBSCRIPTION")
	for _, g := range groups {
		name := clean(g.Name)
		if g.Archived {
			name += " (archived)"
		}
		sub := "-"
		if g.Subscription {
			sub = "updated " + when(g.UpdatedAt)
		}
		fmt.Fprintf(w, "%d\t%s\t%d\t%s\n", g.ID, name, g.ProfileCount, sub)
	}
}

func writeProfiles(w io.Writer, profiles []throne.Profile) {
	if len(profiles) == 0 {
		fmt.Fprintln(w, "No profiles.")
		return
	}
	fmt.Fprintln(w, "\tID\tGROUP\tNAME\tTYPE\tLATENCY\tDOWNLOAD\tUPLOAD\tCOUNTRY\tEXIT IP\tADDRESS")
	for _, p := range profiles {
		mark := ""
		if p.Running {
			mark = "*"
		}
		fmt.Fprintf(w, "%s\t%d\t%d\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", mark, p.ID, p.Group, clean(p.Name),
			dash(p.Type), latency(p.LatencyMS), speed(p.DownloadBPS), speed(p.UploadBPS), dash(p.Country),
			dash(p.IPOut), dash(p.Address))
	}
}

func writeRoutes(w io.Writer, routes []throne.Route) {
	if len(routes) == 0 {
		fmt.Fprintln(w, "No routing profiles.")
		return
	}
	fmt.Fprintln(w, "\tID\tNAME")
	for _, r := range routes {
		mark := ""
		if r.Active {
			mark = "*"
		}
		fmt.Fprintf(w, "%s\t%d\t%s\n", mark, r.ID, clean(r.Name))
	}
}

func writeJob(w io.Writer, j *throne.Job) {
	fmt.Fprintf(w, "Job:\t%d\n", j.ID)
	fmt.Fprintf(w, "Kind:\t%s\n", clean(string(j.Kind)))
	fmt.Fprintf(w, "State:\t%s\n", clean(j.State))
	fmt.Fprintf(w, "Profiles:\t%d\n", len(j.ProfileIDs))
	fmt.Fprintf(w, "Started:\t%s\n", when(j.StartedAt))
	if j.FinishedAt != 0 {
		fmt.Fprintf(w, "Finished:\t%s\n", when(j.FinishedAt))
	}
	if j.State == throne.JobRunning {
		fmt.Fprintf(w, "\nThe test is still running; see it with: thronectl job %d\n", j.ID)
	}
	if len(j.Results) > 0 {
		fmt.Fprintln(w)
		writeProfiles(w, j.Results)
	}
}

func writeJobs(w io.Writer, jobs []throne.Job) {
	if len(jobs) == 0 {
		fmt.Fprintln(w, "No tests.")
		return
	}
	fmt.Fprintln(w, "ID\tKIND\tSTATE\tPROFILES\tSTARTED\tFINISHED")
	for _, j := range jobs {
		fmt.Fprintf(w, "%d\t%s\t%s\t%d\t%s\t%s\n", j.ID, clean(string(j.Kind)), clean(j.State), len(j.ProfileIDs),
			when(j.StartedAt), when(j.FinishedAt))
	}
}

func writeLive(w io.Writer, live *throne.LiveStats) {
	fmt.Fprintln(w, "\tUP\tDOWN")
	fmt.Fprintf(w, "Proxy\t%s/s\t%s/s\n", size(live.Proxy.Up), size(live.Proxy.Down))
	fmt.Fprintf(w, "Direct\t%s/s\t%s/s\n", size(live.Direct.Up), size(live.Direct.Down))
}

func writeTraffic(w io.Writer, t *throne.TrafficStats) {
	fmt.Fprintf(w, "By %s, %s to %s\n", clean(t.By), when(t.From), when(t.To))
	if len(t.Items) == 0 {
		fmt.Fprintln(w, "No traffic.")
		return
	}
	byProfile := t.By != throne.TrafficByApp
	if byProfile {
		fmt.Fprint(w, "ID\t")
	}
	fmt.Fprintln(w, "NAME\tUP\tDOWN\tTOTAL")
	for _, it := range t.Items {
		if byProfile {
			id := "-"
			if it.ID != nil {
				id = strconv.Itoa(*it.ID)
			}
			fmt.Fprintf(w, "%s\t", id)
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", clean(it.Name), size(it.Up), size(it.Down), size(it.Up+it.Down))
	}
}

// latency shows a latency result as Throne does.
func latency(ms int) string {
	switch {
	case ms > 0:
		return fmt.Sprintf("%d ms", ms)
	case ms == 0:
		return "-"
	case ms == -2:
		return "connect only"
	}
	return "unavailable"
}

// speed shows a speed test result, in bits per second, as Mbps.
func speed(bps int64) string {
	switch {
	case bps > 0:
		return fmt.Sprintf("%.2f Mbps", float64(bps)/1e6)
	case bps == 0:
		return "-"
	}
	return "failed"
}

// size shows a byte count in binary units, as Throne does.
func size(n int64) string {
	if n < 1024 {
		return fmt.Sprintf("%d B", n)
	}
	v := float64(n)
	for _, unit := range []string{"KiB", "MiB", "GiB", "TiB", "PiB"} {
		if v /= 1024; v < 1024 {
			return fmt.Sprintf("%.2f %s", v, unit)
		}
	}
	return fmt.Sprintf("%.2f EiB", v/1024)
}

func when(unix int64) string {
	if unix == 0 {
		return "-"
	}
	return time.Unix(unix, 0).Format("2006-01-02 15:04:05")
}

func onOff(on bool) string {
	if on {
		return "on"
	}
	return "off"
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return clean(s)
}

// clean replaces control characters, so that names from subscriptions cannot break the
// table or send escape sequences to the terminal.
func clean(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, s)
}
