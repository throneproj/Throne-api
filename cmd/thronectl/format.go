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
	degraded := "no"
	if st.Degraded != "" {
		degraded = st.Degraded
	}
	var caps []string
	for _, c := range []struct {
		name string
		on   bool
	}{{"tun", st.Capabilities.Tun}, {"kill switch", st.Capabilities.KillSwitch}, {"headless", st.Capabilities.Headless}} {
		if c.on {
			caps = append(caps, c.name)
		}
	}
	for _, row := range [][2]string{
		{"State", st.State},
		{"Profile", profile},
		{"Restart needed", yesNo(st.RestartNeeded)},
		{"Tun", onOff(st.Tun)},
		{"System proxy", onOff(st.SystemProxy)},
		{"Route", fmt.Sprintf("%d %s", st.Route.ID, st.Route.Name)},
		{"Kill switch", st.KillSwitch},
		{"Core", core},
		{"Degraded", degraded},
		{"Inbound", inbound},
		{"Capabilities", list(caps, "none")},
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

func writeImport(w io.Writer, r *throne.ImportResult) {
	if r.State == throne.StateQueued {
		fmt.Fprintf(w, "The import into group %d is still running in Throne; see it with: thronectl profiles --group %d\n",
			r.Group, r.Group)
		return
	}
	fmt.Fprintf(w, "Imported %d profiles into group %d.\n", r.Imported, r.Group)
	if r.Failed > 0 || r.Warned > 0 {
		fmt.Fprintf(w, "%d entries failed; %d were imported with a warning.\n", r.Failed, r.Warned)
	}
	for _, e := range r.Errors {
		fmt.Fprintf(w, "Error: %s\n", clean(e))
	}
	if len(r.Issues) == 0 {
		return
	}
	fmt.Fprintln(w, "\nSEVERITY\tFIELD\tCOUNT\tMESSAGE\tEXAMPLE")
	for _, is := range r.Issues {
		example := "-"
		if len(is.Examples) > 0 {
			example = clean(is.Examples[0])
		}
		fmt.Fprintf(w, "%s\t%s\t%d\t%s\t%s\n", clean(is.Severity), dash(is.Field), is.Count, clean(is.Message), example)
	}
}

// writeAdded shows a group that AddGroup added; updating says whether it was asked to
// download its subscription.
func writeAdded(w io.Writer, r *throne.GroupResult, updating bool) {
	fmt.Fprintf(w, "Added group %d %s.\n", r.Group.ID, clean(r.Group.Name))
	switch {
	case r.Update != nil:
		fmt.Fprintf(w, "Update: %s\n", updateText(r.Update))
	case updating && r.Group.Subscription:
		fmt.Fprintln(w, "The first update is still running in Throne; see it with: thronectl groups")
	}
}

func writeUpdates(w io.Writer, r *throne.UpdateGroupsResult) {
	if len(r.Results) == 0 && len(r.Queued) == 0 {
		fmt.Fprintln(w, "No subscription groups to update.")
		return
	}
	if len(r.Results) > 0 {
		fmt.Fprintln(w, "GROUP\tRESULT")
		for _, u := range r.Results {
			fmt.Fprintf(w, "%d\t%s\n", u.ID, updateText(&u.UpdateResult))
		}
	}
	if len(r.Queued) > 0 {
		ids := make([]string, len(r.Queued))
		for i, id := range r.Queued {
			ids[i] = strconv.Itoa(id)
		}
		fmt.Fprintf(w, "Still updating in Throne: %s; see them with: thronectl groups\n", strings.Join(ids, ", "))
	}
}

func updateText(u *throne.UpdateResult) string {
	if !u.OK {
		return "failed: " + dash(u.Error)
	}
	return fmt.Sprintf("%d added, %d updated, %d removed, %d kept", u.Added, u.Updated, u.Removed, u.Kept)
}

// writeDeletion shows the outcome of a delete; what is the group or the profiles.
func writeDeletion(w io.Writer, d *throne.Deletion, what string) {
	if d.State == throne.StateStopping {
		fmt.Fprintf(w, "Throne is stopping the running profile; it deletes %s after that.\n", what)
		return
	}
	fmt.Fprintf(w, "Deleted %s.\n", what)
}

// writeSettings shows the settings named in keys, in that order.
func writeSettings(w io.Writer, settings map[string]any, keys []string) {
	if len(keys) == 0 {
		fmt.Fprintln(w, "No settings.")
		return
	}
	fmt.Fprintln(w, "SETTING\tVALUE")
	for _, k := range keys {
		fmt.Fprintf(w, "%s\t%s\n", clean(k), settingText(settings[k]))
	}
}

func writeSettingsChange(w io.Writer, ch *throne.SettingsChange) {
	if len(ch.Changed) == 0 {
		fmt.Fprintln(w, "Nothing changed.")
	} else {
		writeSettings(w, ch.Settings, ch.Changed)
	}
	if ch.RestartNeeded {
		fmt.Fprintln(w, "\nRestart the running profile to apply the changes: thronectl connect last --restart")
	}
}

// settingText shows a setting's value; an empty text shows as "".
func settingText(v any) string {
	if s, ok := v.(string); ok {
		if s == "" {
			return `""`
		}
		return clean(s)
	}
	b, err := json.Marshal(v)
	if err != nil {
		return "?"
	}
	return string(b)
}

func writeBackup(w io.Writer, file string, b *throne.BackupInfo) {
	fmt.Fprintf(w, "File:\t%s\n", clean(file))
	fmt.Fprintf(w, "Size:\t%s\n", size(b.Size))
	fmt.Fprintf(w, "SHA-256:\t%s\n", clean(b.SHA256))
	fmt.Fprintf(w, "Parts:\t%s\n", list(b.Parts, "-"))
	fmt.Fprintln(w, "\nThe archive holds credentials in plain text: keep it where only you can read it.")
}

// writeArchive describes an uploaded archive and what a restore of parts does.
func writeArchive(w io.Writer, file string, a *throne.Archive, parts []string) {
	fmt.Fprintf(w, "Archive:\t%s\n", clean(file))
	fmt.Fprintf(w, "Created:\t%s on %s\n", dash(a.CreatedAt), dash(a.Platform))
	fmt.Fprintf(w, "Holds:\t%s\n", list(a.Parts, "-"))
	fmt.Fprintf(w, "Restores:\t%s\n", list(parts, "-"))
	fmt.Fprintln(w, "\nThrone replaces these parts of its data with the archive's, then restarts.")
}

func writeRestore(w io.Writer, r *throne.RestoreResult) {
	if !r.Restarting {
		fmt.Fprintln(w, "Throne is still restoring; it restarts when done.")
		return
	}
	fmt.Fprintf(w, "Restored %s. Throne restarts now.\n", list(r.Restored, "nothing"))
	if r.SkippedRules > 0 {
		fmt.Fprintf(w, "%d routing rules of the archive could not be read and were left out.\n", r.SkippedRules)
	}
}

func writeRuleSets(w io.Writer, r *throne.RuleSetUpdate) {
	if r.State == throne.StateUpdating {
		fmt.Fprintln(w, "The update is still running in Throne.")
		return
	}
	fmt.Fprintf(w, "Updated %d rule-sets.\n", r.Updated)
	if len(r.Failures) == 0 {
		return
	}
	fmt.Fprintln(w, "\nTAG\tERROR")
	for _, f := range r.Failures {
		fmt.Fprintf(w, "%s\t%s\n", dash(f.Tag), dash(f.Error))
	}
}

func writeGeoAssets(w io.Writer, files []throne.GeoAsset) {
	if len(files) == 0 {
		fmt.Fprintln(w, "No geo files.")
		return
	}
	fmt.Fprintln(w, "NAME\tPRESENT\tSIZE\tUPDATED\tURL")
	for _, f := range files {
		present, sz := "no", "-"
		if f.Present {
			present, sz = "yes", size(f.Size)
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", clean(f.Name), present, sz, when(f.UpdatedAt), dash(f.URL))
	}
}

func writeGeoUpdate(w io.Writer, u *throne.GeoUpdate) {
	if u.State == throne.StateDownloading {
		fmt.Fprintln(w, "The downloads are still running in Throne; see them with: thronectl geo")
		return
	}
	fmt.Fprintln(w, "NAME\tRESULT")
	for _, f := range u.Files {
		result := "updated"
		switch {
		case !f.OK:
			result = "failed: " + dash(f.Error)
		case f.Skipped:
			result = "present, skipped"
		}
		fmt.Fprintf(w, "%s\t%s\n", clean(f.Name), result)
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

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

// list joins names for a sentence, or returns none for no names.
func list(names []string, none string) string {
	if len(names) == 0 {
		return none
	}
	return clean(strings.Join(names, ", "))
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
