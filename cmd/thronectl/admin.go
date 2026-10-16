package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"text/tabwriter"
	"time"

	throne "github.com/throneproj/Throne-api"
	"github.com/throneproj/Throne-api/internal/wire"
)

// maxImport is the most bytes import sends from a file or standard input. A request is at
// most 1 MiB, and JSON escapes make the text longer on the way.
const maxImport = 900 << 10

// backupParts are the parts of a backup archive.
var backupParts = []string{
	throne.PartProfiles, throne.PartRoutes, throne.PartSettings, throne.PartOTP, throne.PartIPLists, throne.PartIcons,
}

// geoFiles are the files geo update takes.
var geoFiles = []string{"geoip", "geosite"}

// restoreDefault are the parts backup restore restores without --parts, those the archive
// holds.
var restoreDefault = []string{throne.PartProfiles, throne.PartRoutes, throne.PartOTP, throne.PartIPLists}

func cmdImport(c *cli, fs *flag.FlagSet, args []string) error {
	var group optInt
	fs.Var(&group, "group", "import into this group `ID` (default: the group Throne shows)")
	link := fs.String("url", "", "make Throne download this http or https `URL` and import what it returns")
	noWait := fs.Bool("no-wait", false, "return at once instead of waiting for the import")
	pos, err := c.parse(fs, args, 0, 1)
	if err != nil {
		return err
	}
	var src throne.ImportSource
	switch {
	case len(pos) == 1 && *link != "":
		return c.usageErr("give FILE, - or --url, not two of them")
	case *link != "":
		if !httpURL(*link) {
			return c.usageErr("--url takes an http or https URL, not %q", *link)
		}
		src.URL = *link
	case len(pos) == 0:
		return c.usageErr("give FILE, - for standard input, or --url")
	default:
		if src.Text, err = c.readImport(pos[0]); err != nil {
			return err
		}
	}
	if group.set {
		src.Group = &group.v
	}
	cl, ctx, err := c.open()
	if err != nil {
		return err
	}
	res, err := cl.Import(ctx, src, waitOptions(*noWait)...)
	if errors.Is(err, wire.ErrTooLarge) && len(pos) == 1 {
		return c.usageErr("%s is too large for one request: split it, or serve it over http and use --url", inputName(pos[0]))
	}
	if err != nil {
		return err
	}
	return c.show(func(w io.Writer) { writeImport(w, res) })
}

// readImport reads the text to import from a file, or from standard input for "-".
func (c *cli) readImport(name string) (string, error) {
	r, what := c.stdin, inputName(name)
	if name != "-" {
		f, err := os.Open(name)
		if err != nil {
			return "", err
		}
		defer f.Close()
		r = f
	}
	b, err := io.ReadAll(io.LimitReader(r, maxImport+1))
	if err != nil {
		return "", fmt.Errorf("read %s: %w", what, err)
	}
	switch {
	case len(b) > maxImport:
		return "", c.usageErr("%s holds more than %d KiB, more than one request carries: split it, or serve it "+
			"over http and use --url", what, maxImport>>10)
	case len(bytes.TrimSpace(b)) == 0:
		return "", c.usageErr("%s is empty", what)
	}
	// Some Windows editors start the file with a byte order mark.
	return string(bytes.TrimPrefix(b, []byte{0xEF, 0xBB, 0xBF})), nil
}

func inputName(name string) string {
	if name == "-" {
		return "standard input"
	}
	return name
}

// httpURL says whether s is an http or https URL with a host.
func httpURL(s string) bool {
	u, err := url.Parse(s)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != ""
}

func cmdSub(c *cli, fs *flag.FlagSet, args []string) error {
	name := fs.String("name", "", "add: the group's `name` (default: the URL's host)")
	noAuto := fs.Bool("no-auto-update", false, "add: leave the group out of Throne's automatic updates")
	noUpdate := fs.Bool("no-update", false, "add: add the group without downloading it now")
	all := fs.Bool("all", false, "update: every subscription group that is not archived")
	noWait := fs.Bool("no-wait", false, "return at once instead of waiting for the update")
	pos, err := c.parse(fs, args, 1, -1)
	if err != nil {
		return err
	}
	switch pos[0] {
	case "add":
		switch {
		case *all:
			return c.usageErr("--all goes with sub update")
		case len(pos) < 2:
			return c.usageErr("missing URL")
		case len(pos) > 2:
			return c.usageErr("unexpected argument %q", pos[2])
		}
		return subAdd(c, pos[1], *name, *noAuto, *noUpdate, *noWait)
	case "update":
		if *name != "" || *noAuto || *noUpdate {
			return c.usageErr("--name, --no-auto-update and --no-update go with sub add")
		}
		return subUpdate(c, pos[1:], *all, *noWait)
	}
	return c.usageErr("want add or update, not %q", pos[0])
}

func subAdd(c *cli, link, name string, noAuto, noUpdate, noWait bool) error {
	u, err := url.Parse(link)
	if err != nil || !httpURL(link) {
		return c.usageErr("want an http or https URL, not %q", link)
	}
	if name == "" {
		name = u.Hostname()
	}
	cl, ctx, err := c.open()
	if err != nil {
		return err
	}
	g := throne.NewGroup{Name: name, URL: link, SkipAutoUpdate: noAuto, SkipUpdate: noUpdate}
	res, err := cl.AddGroup(ctx, g, waitOptions(noWait)...)
	if err != nil {
		return err
	}
	if err := c.show(func(w io.Writer) { writeAdded(w, res, !noUpdate) }); err != nil {
		return err
	}
	if res.Update != nil && !res.Update.OK {
		return failure{fmt.Sprintf("group %d was added, but its first update failed", res.Group.ID)}
	}
	return nil
}

func subUpdate(c *cli, args []string, all, noWait bool) error {
	switch {
	case all && len(args) > 0:
		return c.usageErr("give group IDs or --all, not both")
	case !all && len(args) == 0:
		return c.usageErr("give group IDs or --all")
	}
	ids, err := parseIDs(args)
	if err != nil {
		return c.usageErr("%v", err)
	}
	cl, ctx, err := c.open()
	if err != nil {
		return err
	}
	var res *throne.UpdateGroupsResult
	if all {
		res, err = cl.UpdateGroups(ctx, waitOptions(noWait)...)
	} else {
		res, err = c.updateEach(ctx, cl, ids, waitOptions(noWait))
	}
	if err != nil {
		return err
	}
	if err := c.show(func(w io.Writer) { writeUpdates(w, res) }); err != nil {
		return err
	}
	failed := 0
	for _, u := range res.Results {
		if !u.OK {
			failed++
		}
	}
	if failed > 0 {
		return failure{fmt.Sprintf("%d of %d updates failed", failed, len(res.Results))}
	}
	return nil
}

// updateEach updates groups one after another and gathers the outcomes as UpdateGroups
// returns them, which --json prints. A failed update of a single group is its error.
func (c *cli) updateEach(ctx context.Context, cl *throne.Client, ids []int, opts []throne.Option) (*throne.UpdateGroupsResult, error) {
	res := &throne.UpdateGroupsResult{}
	for _, id := range ids {
		r, err := cl.UpdateGroup(ctx, id, opts...)
		var apiErr *throne.APIError
		switch {
		case errors.As(err, &apiErr) && len(ids) > 1:
			msg := apiErr.Message
			if msg == "" {
				msg = apiErr.Code
			}
			res.Results = append(res.Results, throne.GroupUpdate{ID: id, UpdateResult: throne.UpdateResult{Error: msg}})
		case err != nil:
			return nil, err
		case r.Update == nil:
			res.Queued = append(res.Queued, id)
		default:
			res.Results = append(res.Results, throne.GroupUpdate{ID: id, UpdateResult: *r.Update})
		}
	}
	raw, err := json.Marshal(res)
	if err != nil {
		return nil, err
	}
	c.raw = raw
	return res, nil
}

func cmdGroup(c *cli, fs *flag.FlagSet, args []string) error {
	noWait := fs.Bool("no-wait", false, "rm: return at once instead of waiting for the running profile to stop")
	pos, err := c.parse(fs, args, 2, 2)
	if err != nil {
		return err
	}
	switch pos[0] {
	case "add":
		if *noWait {
			return c.usageErr("--no-wait goes with group rm")
		}
		cl, ctx, err := c.open()
		if err != nil {
			return err
		}
		res, err := cl.AddGroup(ctx, throne.NewGroup{Name: pos[1]})
		if err != nil {
			return err
		}
		return c.show(func(w io.Writer) { writeAdded(w, res, false) })
	case "rm":
		id, err := c.id(pos[1])
		if err != nil {
			return err
		}
		cl, ctx, err := c.open()
		if err != nil {
			return err
		}
		d, err := cl.DeleteGroup(ctx, id, waitOptions(*noWait)...)
		if err != nil {
			return err
		}
		return c.show(func(w io.Writer) { writeDeletion(w, d, fmt.Sprintf("group %d", id)) })
	}
	return c.usageErr("want add or rm, not %q", pos[0])
}

func cmdProfile(c *cli, fs *flag.FlagSet, args []string) error {
	var group optInt
	fs.Var(&group, "group", "rm: the profiles of this group `ID`")
	name := fs.String("name", "", "rm: with --group, only the profiles whose names match this `regex`")
	noWait := fs.Bool("no-wait", false, "return at once instead of waiting for the running profile to stop")
	pos, err := c.parse(fs, args, 1, -1)
	if err != nil {
		return err
	}
	if pos[0] != "rm" {
		return c.usageErr("want rm, not %q", pos[0])
	}
	sel, err := c.selection(group, *name, pos[1:])
	if err != nil {
		return err
	}
	cl, ctx, err := c.open()
	if err != nil {
		return err
	}
	d, err := cl.DeleteProfiles(ctx, sel, waitOptions(*noWait)...)
	if err != nil {
		return err
	}
	return c.show(func(w io.Writer) { writeDeletion(w, d, fmt.Sprintf("%d profiles", d.Deleted)) })
}

func cmdSettings(c *cli, fs *flag.FlagSet, args []string) error {
	pos, err := c.parse(fs, args, 0, -1)
	if err != nil {
		return err
	}
	if len(pos) > 0 && pos[0] == "set" {
		return setSettings(c, pos[1:])
	}
	keys := pos
	if len(pos) > 0 && pos[0] == "get" {
		keys = pos[1:]
	}
	cl, ctx, err := c.open()
	if err != nil {
		return err
	}
	settings, err := cl.Settings(ctx)
	if err != nil {
		return err
	}
	for _, k := range keys {
		if _, ok := settings[k]; !ok {
			return c.usageErr("no setting %q here; run 'thronectl settings' for the list", k)
		}
	}
	if len(keys) == 0 {
		for k := range settings {
			keys = append(keys, k)
		}
		slices.Sort(keys)
	} else if c.json {
		if err := c.pickSettings(keys); err != nil {
			return err
		}
	}
	return c.show(func(w io.Writer) { writeSettings(w, settings, keys) })
}

// pickSettings reduces the response body that --json prints to the settings in keys, with
// their values as Throne sent them.
func (c *cli) pickSettings(keys []string) error {
	var all struct {
		Settings map[string]json.RawMessage `json:"settings"`
	}
	if err := json.Unmarshal(c.raw, &all); err != nil {
		return fmt.Errorf("decode settings: %w", err)
	}
	picked := map[string]json.RawMessage{}
	for _, k := range keys {
		picked[k] = all.Settings[k]
	}
	raw, err := json.Marshal(map[string]any{"settings": picked})
	if err != nil {
		return err
	}
	c.raw = raw
	return nil
}

func setSettings(c *cli, args []string) error {
	if len(args) == 0 {
		return c.usageErr("missing KEY=VALUE")
	}
	values := map[string]any{}
	for _, kv := range args {
		k, v, ok := strings.Cut(kv, "=")
		if !ok || k == "" {
			return c.usageErr("want settings as KEY=VALUE, not %q", kv)
		}
		if _, dup := values[k]; dup {
			return c.usageErr("setting %q given twice", k)
		}
		values[k] = settingValue(v)
	}
	cl, ctx, err := c.open()
	if err != nil {
		return err
	}
	ch, err := cl.SetSettings(ctx, values)
	if err != nil {
		return err
	}
	return c.show(func(w io.Writer) { writeSettingsChange(w, ch) })
}

// settingValue turns the text of a setting into what Throne gets: true and false are
// bools, a JSON number is a number, anything else is the text itself.
func settingValue(s string) any {
	switch {
	case s == "true", s == "false":
		return s == "true"
	case s != "" && strings.ContainsRune("-0123456789", rune(s[0])) && strings.TrimSpace(s) == s && json.Valid([]byte(s)):
		return json.Number(s)
	}
	return s
}

func cmdBackup(c *cli, fs *flag.FlagSet, args []string) error {
	out := fs.String("o", "", "create: write the archive to `file` (default: throne-backup-<time>.thrbackup here)")
	partList := fs.String("parts", "", "the parts, a comma-separated `list` (default: create every part, "+
		"restore profiles,routes,otp,ip_lists)")
	yes := fs.Bool("yes", false, "restore: do not ask for confirmation")
	pos, err := c.parse(fs, args, 1, 2)
	if err != nil {
		return err
	}
	var parts []string
	if *partList != "" {
		if parts, err = parseParts(*partList); err != nil {
			return c.usageErr("--parts: %v", err)
		}
	}
	switch pos[0] {
	case "create":
		switch {
		case *yes:
			return c.usageErr("--yes goes with backup restore")
		case len(pos) > 1:
			return c.usageErr("unexpected argument %q", pos[1])
		}
		return backupCreate(c, *out, parts)
	case "restore":
		switch {
		case *out != "":
			return c.usageErr("-o goes with backup create")
		case len(pos) < 2:
			return c.usageErr("missing FILE")
		}
		return backupRestore(c, pos[1], parts, *yes)
	}
	return c.usageErr("want create or restore, not %q", pos[0])
}

// parseParts parses a comma-separated list of parts; ip-lists stands for ip_lists.
func parseParts(list string) ([]string, error) {
	var parts []string
	for p := range strings.SplitSeq(list, ",") {
		p = strings.ReplaceAll(strings.ToLower(strings.TrimSpace(p)), "-", "_")
		if !slices.Contains(backupParts, p) {
			return nil, fmt.Errorf("unknown part %q; the parts are %s", p, strings.Join(backupParts, ", "))
		}
		if !slices.Contains(parts, p) {
			parts = append(parts, p)
		}
	}
	return parts, nil
}

func backupCreate(c *cli, out string, parts []string) error {
	if out == "" {
		out = "throne-backup-" + time.Now().Format("20060102-150405") + ".thrbackup"
	}
	cl, ctx, err := c.open()
	if err != nil {
		return err
	}
	// The archive gets its name only once it is complete and checked; CreateTemp makes
	// the file readable by the owner only.
	tmp, err := os.CreateTemp(filepath.Dir(out), ".throne-backup-*")
	if err != nil {
		return err
	}
	info, err := cl.Backup(ctx, tmp, parts)
	if err == nil {
		err = tmp.Sync()
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp.Name(), out)
	}
	if err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return c.show(func(w io.Writer) { writeBackup(w, out, info) })
}

func backupRestore(c *cli, file string, parts []string, yes bool) error {
	if err := c.canConfirm(yes, "restore"); err != nil {
		return err
	}
	cl, ctx, err := c.open()
	if err != nil {
		return err
	}
	f, err := os.Open(file)
	if err != nil {
		return err
	}
	defer f.Close()
	magic := make([]byte, 4)
	if _, err := io.ReadFull(f, magic); err != nil || string(magic) != "THRN" {
		return fmt.Errorf("%s is not a Throne backup archive", file)
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return err
	}
	// Waits while Throne checks the archive, and discards the upload after an error.
	up, err := cl.UploadRestore(ctx, f)
	if err != nil {
		return err
	}
	if parts, err = c.restoreParts(up.Archive.Parts, parts); err != nil {
		discardUpload(cl, up.ID)
		return err
	}
	if !yes {
		tw := tabwriter.NewWriter(c.stderr, 0, 0, 2, ' ', 0)
		writeArchive(tw, file, up.Archive, parts)
		tw.Flush()
		if !c.ask("Restore?") {
			discardUpload(cl, up.ID)
			return failure{"not confirmed; nothing was restored"}
		}
	}
	res, err := cl.ApplyRestore(c.context(), up.ID, parts)
	if err != nil {
		discardUpload(cl, up.ID)
		return err
	}
	return c.show(func(w io.Writer) { writeRestore(w, res) })
}

// restoreParts picks the parts to restore of those the archive has: the ones asked for,
// which it must have, or else the default ones it has.
func (c *cli) restoreParts(has, asked []string) ([]string, error) {
	if asked == nil {
		for _, p := range restoreDefault {
			if slices.Contains(has, p) {
				asked = append(asked, p)
			}
		}
		if len(asked) == 0 {
			return nil, c.usageErr("the archive has none of %s, only %s: choose with --parts",
				strings.Join(restoreDefault, ", "), list(has, "nothing"))
		}
		return asked, nil
	}
	for _, p := range asked {
		if !slices.Contains(has, p) {
			return nil, c.usageErr("the archive has no %s, only %s", p, list(has, "nothing"))
		}
	}
	return asked, nil
}

// discardUpload deletes an upload in Throne, best effort.
func discardUpload(cl *throne.Client, id int) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cl.CancelRestore(ctx, id)
}

func cmdRuleSets(c *cli, fs *flag.FlagSet, args []string) error {
	noWait := fs.Bool("no-wait", false, "return at once instead of waiting for the update")
	pos, err := c.parse(fs, args, 1, 1)
	if err != nil {
		return err
	}
	if pos[0] != "update" {
		return c.usageErr("want update, not %q", pos[0])
	}
	cl, ctx, err := c.open()
	if err != nil {
		return err
	}
	res, err := cl.UpdateRuleSets(ctx, waitOptions(*noWait)...)
	if err != nil {
		return err
	}
	if err := c.show(func(w io.Writer) { writeRuleSets(w, res) }); err != nil {
		return err
	}
	if n := len(res.Failures); n > 0 {
		return failure{fmt.Sprintf("%d rule-sets failed to update", n)}
	}
	return nil
}

func cmdGeo(c *cli, fs *flag.FlagSet, args []string) error {
	onlyMissing := fs.Bool("only-missing", false, "update: only the files that are missing")
	noWait := fs.Bool("no-wait", false, "update: return at once instead of waiting for the downloads")
	pos, err := c.parse(fs, args, 0, -1)
	if err != nil {
		return err
	}
	if len(pos) == 0 || pos[0] == "status" {
		switch {
		case *onlyMissing || *noWait:
			return c.usageErr("--only-missing and --no-wait go with geo update")
		case len(pos) > 1:
			return c.usageErr("unexpected argument %q", pos[1])
		}
		cl, ctx, err := c.open()
		if err != nil {
			return err
		}
		files, err := cl.GeoAssets(ctx)
		if err != nil {
			return err
		}
		return c.show(func(w io.Writer) { writeGeoAssets(w, files) })
	}
	if pos[0] != "update" {
		return c.usageErr("want status or update, not %q", pos[0])
	}
	var files []string
	for _, f := range pos[1:] {
		if !slices.Contains(geoFiles, f) {
			return c.usageErr("want geoip or geosite, not %q", f)
		}
		if !slices.Contains(files, f) {
			files = append(files, f)
		}
	}
	opts := waitOptions(*noWait)
	if *onlyMissing {
		opts = append(opts, throne.OnlyMissing())
	}
	cl, ctx, err := c.open()
	if err != nil {
		return err
	}
	res, err := cl.UpdateGeoAssets(ctx, files, opts...)
	if err != nil {
		return err
	}
	if err := c.show(func(w io.Writer) { writeGeoUpdate(w, res) }); err != nil {
		return err
	}
	failed := 0
	for _, f := range res.Files {
		if !f.OK {
			failed++
		}
	}
	if failed > 0 {
		return failure{fmt.Sprintf("%d of %d files failed to update", failed, len(res.Files))}
	}
	return nil
}

func cmdRestart(c *cli, fs *flag.FlagSet, args []string) error {
	if _, err := c.parse(fs, args, 0, 0); err != nil {
		return err
	}
	cl, ctx, err := c.open()
	if err != nil {
		return err
	}
	resume, err := cl.Restart(ctx)
	if err != nil {
		return err
	}
	return c.show(func(w io.Writer) {
		if resume {
			fmt.Fprintln(w, "Throne is restarting; it starts the running profile again.")
		} else {
			fmt.Fprintln(w, "Throne is restarting.")
		}
	})
}

func cmdShutdown(c *cli, fs *flag.FlagSet, args []string) error {
	yes := fs.Bool("yes", false, "do not ask for confirmation")
	if _, err := c.parse(fs, args, 0, 0); err != nil {
		return err
	}
	if err := c.canConfirm(*yes, "shut Throne down"); err != nil {
		return err
	}
	cl, _, err := c.open()
	if err != nil {
		return err
	}
	if !*yes && !c.ask("Shut Throne down?") {
		return failure{"not confirmed; Throne keeps running"}
	}
	if err := cl.Shutdown(c.context()); err != nil {
		return err
	}
	return c.show(func(w io.Writer) { fmt.Fprintln(w, "Throne is exiting.") })
}

func cmdKeygen(c *cli, fs *flag.FlagSet, args []string) error {
	out := fs.String("o", "", "write the key to `file`, readable by you only, instead of printing it")
	if _, err := c.parse(fs, args, 0, 0); err != nil {
		return err
	}
	b := make([]byte, wire.KeySize)
	if _, err := rand.Read(b); err != nil {
		return err
	}
	key := base64.RawURLEncoding.EncodeToString(b) + "\n"
	if *out == "" {
		_, err := io.WriteString(c.stdout, key)
		return err
	}
	f, err := os.OpenFile(*out, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, os.ErrExist) {
		return fmt.Errorf("%s exists; keygen does not overwrite it", *out)
	}
	if err != nil {
		return err
	}
	_, err = io.WriteString(f, key)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(*out)
		return err
	}
	_, err = fmt.Fprintf(c.stdout, "Wrote a new key to %s.\n", *out)
	return err
}
