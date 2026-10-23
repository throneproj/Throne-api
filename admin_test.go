package throne_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	throne "github.com/throneproj/Throne-api"
	"github.com/throneproj/Throne-api/internal/rawbody"
	"github.com/throneproj/Throne-api/internal/testserver"
)

func TestStatusDegraded(t *testing.T) {
	body := strings.Replace(statusJSON, `"degraded":null`, `"degraded":"kill_switch_failed"`, 1)
	c, _ := serve(t, func(context.Context, *testserver.Request) *testserver.Response {
		return testserver.OK(json.RawMessage(body))
	})
	st, err := c.Status(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if st.Degraded != throne.DegradedKillSwitchFailed || !st.RestartNeeded || !st.Capabilities.Headless {
		t.Fatalf("Status = %+v", st)
	}
}

func TestAdminRequests(t *testing.T) {
	rec := &recorder{answer: always(testserver.OK(json.RawMessage(`{}`)))}
	c, _ := serve(t, rec.handle)
	zero := 0
	tests := []struct {
		name   string
		call   func(ctx context.Context) error
		method string
		path   string
		body   string
	}{
		{"import text", func(ctx context.Context) error {
			_, err := c.Import(ctx, throne.ImportSource{Text: "vless://a\nss://b", Group: &zero})
			return err
		}, "POST", "/v1/import", `{"as":"profiles","text":"vless://a\nss://b","group":0}`},
		{"import url", func(ctx context.Context) error {
			_, err := c.Import(ctx, throne.ImportSource{URL: "https://example.com/list.txt"}, throne.NoWait())
			return err
		}, "POST", "/v1/import", `{"as":"url","url":"https://example.com/list.txt","wait":false}`},
		{"add group", func(ctx context.Context) error { _, err := c.AddGroup(ctx, throne.NewGroup{Name: "Mine"}); return err },
			"POST", "/v1/groups", `{"name":"Mine"}`},
		{"add subscription", func(ctx context.Context) error {
			_, err := c.AddGroup(ctx, throne.NewGroup{Name: "Provider", URL: "https://sub.example/x", SkipAutoUpdate: true, SkipUpdate: true})
			return err
		}, "POST", "/v1/groups", `{"name":"Provider","url":"https://sub.example/x","auto_update":false,"update":false}`},
		{"update group 0", func(ctx context.Context) error { _, err := c.UpdateGroup(ctx, 0); return err },
			"POST", "/v1/groups/0/update", ""},
		{"update groups", func(ctx context.Context) error { _, err := c.UpdateGroups(ctx, throne.NoWait()); return err },
			"POST", "/v1/groups/update", `{"wait":false}`},
		{"delete group", func(ctx context.Context) error { _, err := c.DeleteGroup(ctx, 3); return err },
			"DELETE", "/v1/groups/3", ""},
		{"delete profiles by id", func(ctx context.Context) error {
			_, err := c.DeleteProfiles(ctx, throne.SelectProfiles(0, 7))
			return err
		}, "DELETE", "/v1/profiles", `{"profiles":[0,7]}`},
		{"delete profiles of a group", func(ctx context.Context) error {
			_, err := c.DeleteProfiles(ctx, throne.SelectGroup(0, "DE"), throne.NoWait())
			return err
		}, "DELETE", "/v1/profiles", `{"group":0,"name":"DE","wait":false}`},
		{"settings", func(ctx context.Context) error { _, err := c.Settings(ctx); return err },
			"GET", "/v1/settings", ""},
		{"set settings", func(ctx context.Context) error {
			_, err := c.SetSettings(ctx, map[string]any{"log_level": "debug", "tun_mtu": 1400, "kill_switch": true})
			return err
		}, "PUT", "/v1/settings", `{"settings":{"log_level":"debug","tun_mtu":1400,"kill_switch":true}}`},
		{"set no settings", func(ctx context.Context) error { _, err := c.SetSettings(ctx, nil); return err },
			"PUT", "/v1/settings", `{"settings":{}}`},
		{"update rule-sets", func(ctx context.Context) error { _, err := c.UpdateRuleSets(ctx); return err },
			"POST", "/v1/rule-sets/update", ""},
		{"geo assets", func(ctx context.Context) error { _, err := c.GeoAssets(ctx); return err },
			"GET", "/v1/geo-assets", ""},
		{"update geo assets", func(ctx context.Context) error { _, err := c.UpdateGeoAssets(ctx, nil); return err },
			"POST", "/v1/geo-assets/update", ""},
		{"update missing geoip", func(ctx context.Context) error {
			_, err := c.UpdateGeoAssets(ctx, []string{"geoip"}, throne.OnlyMissing())
			return err
		}, "POST", "/v1/geo-assets/update", `{"files":["geoip"],"only_missing":true}`},
		{"restart", func(ctx context.Context) error { _, err := c.Restart(ctx); return err },
			"POST", "/v1/restart", ""},
		{"shutdown", func(ctx context.Context) error { return c.Shutdown(ctx) },
			"POST", "/v1/shutdown", ""},
		{"apply restore", func(ctx context.Context) error { _, err := c.ApplyRestore(ctx, 2, nil); return err },
			"POST", "/v1/restores/2/apply", ""},
		{"apply restore parts", func(ctx context.Context) error {
			_, err := c.ApplyRestore(ctx, 2, []string{throne.PartProfiles, throne.PartIPLists}, throne.NoWait())
			return err
		}, "POST", "/v1/restores/2/apply", `{"parts":["profiles","ip_lists"],"wait":false}`},
		{"restore status", func(ctx context.Context) error { _, err := c.RestoreStatus(ctx, 2); return err },
			"GET", "/v1/restores/2", ""},
		{"cancel restore", func(ctx context.Context) error { return c.CancelRestore(ctx, 2) },
			"DELETE", "/v1/restores/2", ""},
	}
	for _, tt := range tests {
		if err := tt.call(context.Background()); err != nil {
			t.Errorf("%s: %v", tt.name, err)
			continue
		}
		r := rec.last(t)
		if r.Method != tt.method || r.Path != tt.path || r.Query != nil {
			t.Errorf("%s: request %s %s %v, want %s %s", tt.name, r.Method, r.Path, r.Query, tt.method, tt.path)
		}
		jsonEqual(t, tt.name+": body", r.Body, tt.body)
	}

	n := len(rec.requests())
	if _, err := c.Import(t.Context(), throne.ImportSource{Text: "vless://a", URL: "https://x"}); err == nil {
		t.Error("Import accepted both Text and URL")
	}
	if len(rec.requests()) != n {
		t.Error("a request that should fail locally reached the server")
	}
}

func TestAdminWaitFields(t *testing.T) {
	rec := &recorder{answer: always(testserver.OK(json.RawMessage(`{}`)))}
	c, _ := serve(t, rec.handle)
	waits := map[string]func(ctx context.Context, opts ...throne.Option) error{
		"import": func(ctx context.Context, opts ...throne.Option) error {
			_, err := c.Import(ctx, throne.ImportSource{Text: "vless://a"}, opts...)
			return err
		},
		"add group": func(ctx context.Context, opts ...throne.Option) error {
			_, err := c.AddGroup(ctx, throne.NewGroup{Name: "x", URL: "https://x"}, opts...)
			return err
		},
		"update group": func(ctx context.Context, opts ...throne.Option) error {
			_, err := c.UpdateGroup(ctx, 1, opts...)
			return err
		},
		"update groups": func(ctx context.Context, opts ...throne.Option) error {
			_, err := c.UpdateGroups(ctx, opts...)
			return err
		},
		"delete group": func(ctx context.Context, opts ...throne.Option) error {
			_, err := c.DeleteGroup(ctx, 1, opts...)
			return err
		},
		"delete profiles": func(ctx context.Context, opts ...throne.Option) error {
			_, err := c.DeleteProfiles(ctx, throne.SelectProfiles(1), opts...)
			return err
		},
		"update rule-sets": func(ctx context.Context, opts ...throne.Option) error {
			_, err := c.UpdateRuleSets(ctx, opts...)
			return err
		},
		"update geo assets": func(ctx context.Context, opts ...throne.Option) error {
			_, err := c.UpdateGeoAssets(ctx, nil, opts...)
			return err
		},
		"apply restore": func(ctx context.Context, opts ...throne.Option) error {
			_, err := c.ApplyRestore(ctx, 2, nil, opts...)
			return err
		},
	}
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	for name, call := range waits {
		if err := call(ctx); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		m := rec.last(t).Fields()
		if to, _ := m["timeout"].(float64); to < 16 || to > 18 || m["wait"] != nil {
			t.Errorf("%s: body %s, want a timeout from the deadline", name, rec.last(t).Body)
		}
		if err := call(ctx, throne.NoWait()); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		m = rec.last(t).Fields()
		if _, ok := m["timeout"]; ok || m["wait"] != false {
			t.Errorf("%s with NoWait: body %s", name, rec.last(t).Body)
		}
	}

	others := map[string]func(ctx context.Context) error{
		"set settings": func(ctx context.Context) error {
			_, err := c.SetSettings(ctx, map[string]any{"log_level": "info"})
			return err
		},
		"restart":        func(ctx context.Context) error { _, err := c.Restart(ctx); return err },
		"shutdown":       func(ctx context.Context) error { return c.Shutdown(ctx) },
		"cancel restore": func(ctx context.Context) error { return c.CancelRestore(ctx, 2) },
		"restore status": func(ctx context.Context) error { _, err := c.RestoreStatus(ctx, 2); return err },
	}
	for name, call := range others {
		if err := call(ctx); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if m := rec.last(t).Fields(); m["timeout"] != nil || m["wait"] != nil {
			t.Errorf("%s: body %s has waiting fields", name, rec.last(t).Body)
		}
	}
}

func TestAdminResponses(t *testing.T) {
	var resp atomic.Pointer[testserver.Response]
	c, _ := serve(t, func(context.Context, *testserver.Request) *testserver.Response { return resp.Load() })
	group := throne.Group{ID: 3, Name: "Provider", Subscription: true, ProfileCount: 12, UpdatedAt: 1760000000}
	groupJSON := `{"id":3,"name":"Provider","subscription":true,"archived":false,"profile_count":12,"updated_at":1760000000}`
	tests := []struct {
		name string
		resp *testserver.Response
		call func(ctx context.Context) (any, error)
		want any
	}{
		{"import", testserver.OK(json.RawMessage(`{"group":3,"imported":2,"profile_ids":[40,41],"failed":1,"warned":0,` +
			`"issues":[{"severity":"error","field":"port","message":"Port out of range","count":1,"examples":["vless://h:70000 #x"]}],` +
			`"errors":[]}`)),
			func(ctx context.Context) (any, error) { return c.Import(ctx, throne.ImportSource{Text: "x"}) },
			&throne.ImportResult{Group: 3, Imported: 2, ProfileIDs: []int{40, 41}, Failed: 1, Errors: []string{},
				Issues: []throne.ImportIssue{{Severity: throne.SeverityError, Field: "port", Message: "Port out of range",
					Count: 1, Examples: []string{"vless://h:70000 #x"}}}}},
		{"import queued", testserver.Accepted(map[string]any{"group": 3, "state": "queued"}),
			func(ctx context.Context) (any, error) { return c.Import(ctx, throne.ImportSource{Text: "x"}) },
			&throne.ImportResult{Group: 3, State: throne.StateQueued}},
		{"add group failed update", testserver.OK(json.RawMessage(`{"group":` + groupJSON + `,"update":{"ok":false,"error":"HTTP 403"}}`)),
			func(ctx context.Context) (any, error) {
				return c.AddGroup(ctx, throne.NewGroup{Name: "Provider", URL: "https://x"})
			},
			&throne.GroupResult{Group: group, Update: &throne.UpdateResult{Error: "HTTP 403"}}},
		{"add plain group", testserver.OK(json.RawMessage(`{"group":` + groupJSON + `,"update":null}`)),
			func(ctx context.Context) (any, error) { return c.AddGroup(ctx, throne.NewGroup{Name: "Provider"}) },
			&throne.GroupResult{Group: group}},
		{"update group", testserver.OK(json.RawMessage(`{"group":` + groupJSON + `,"update":{"ok":true,"added":3,"updated":10,"removed":1,"kept":1}}`)),
			func(ctx context.Context) (any, error) { return c.UpdateGroup(ctx, 3) },
			&throne.GroupResult{Group: group, Update: &throne.UpdateResult{OK: true, Added: 3, Updated: 10, Removed: 1, Kept: 1}}},
		{"update group accepted", testserver.Accepted(json.RawMessage(`{"group":` + groupJSON + `}`)),
			func(ctx context.Context) (any, error) { return c.UpdateGroup(ctx, 3) },
			&throne.GroupResult{Group: group}},
		{"update groups", testserver.OK(json.RawMessage(`{"results":[{"id":0,"ok":true,"added":1,"updated":2,"removed":3,"kept":0},` +
			`{"id":4,"ok":false,"error":"timeout"}]}`)),
			func(ctx context.Context) (any, error) { return c.UpdateGroups(ctx) },
			&throne.UpdateGroupsResult{Results: []throne.GroupUpdate{
				{ID: 0, UpdateResult: throne.UpdateResult{OK: true, Added: 1, Updated: 2, Removed: 3}},
				{ID: 4, UpdateResult: throne.UpdateResult{Error: "timeout"}}}}},
		{"update no groups", testserver.OK(json.RawMessage(`{"results":[]}`)),
			func(ctx context.Context) (any, error) { return c.UpdateGroups(ctx) },
			&throne.UpdateGroupsResult{Results: []throne.GroupUpdate{}}},
		{"update groups queued", testserver.Accepted(json.RawMessage(`{"queued":[3,4]}`)),
			func(ctx context.Context) (any, error) { return c.UpdateGroups(ctx) },
			&throne.UpdateGroupsResult{Queued: []int{3, 4}}},
		{"delete group", testserver.OK(json.RawMessage(`{"deleted":3}`)),
			func(ctx context.Context) (any, error) { return c.DeleteGroup(ctx, 3) },
			&throne.Deletion{Deleted: 3}},
		{"delete profiles stopping", testserver.Accepted(json.RawMessage(`{"deleted":5,"state":"stopping"}`)),
			func(ctx context.Context) (any, error) { return c.DeleteProfiles(ctx, throne.SelectGroup(1, "")) },
			&throne.Deletion{Deleted: 5, State: throne.StateStopping}},
		{"settings", testserver.OK(json.RawMessage(`{"settings":{"log_level":"info","tun_mtu":9000,"kill_switch":false}}`)),
			func(ctx context.Context) (any, error) { return c.Settings(ctx) },
			map[string]any{"log_level": "info", "tun_mtu": 9000.0, "kill_switch": false}},
		{"set settings", testserver.OK(json.RawMessage(`{"settings":{"log_level":"debug"},"changed":["log_level"],"restart_needed":true}`)),
			func(ctx context.Context) (any, error) {
				return c.SetSettings(ctx, map[string]any{"log_level": "debug"})
			},
			&throne.SettingsChange{Settings: map[string]any{"log_level": "debug"}, Changed: []string{"log_level"}, RestartNeeded: true}},
		{"rule-sets", testserver.OK(json.RawMessage(`{"updated":3,"failures":[{"tag":"geosite-x","error":"404"}]}`)),
			func(ctx context.Context) (any, error) { return c.UpdateRuleSets(ctx) },
			&throne.RuleSetUpdate{Updated: 3, Failures: []throne.RuleSetFailure{{Tag: "geosite-x", Error: "404"}}}},
		{"rule-sets updating", testserver.Accepted(json.RawMessage(`{"state":"updating"}`)),
			func(ctx context.Context) (any, error) { return c.UpdateRuleSets(ctx) },
			&throne.RuleSetUpdate{State: throne.StateUpdating}},
		{"geo assets", testserver.OK(json.RawMessage(`{"files":[{"name":"geoip.dat","url":"https://x/geoip.dat","present":true,` +
			`"size":1234,"updated_at":1760000000},{"name":"geosite.dat","url":"https://x/geosite.dat","present":false,"size":0,"updated_at":0}]}`)),
			func(ctx context.Context) (any, error) { return c.GeoAssets(ctx) },
			[]throne.GeoAsset{{Name: "geoip.dat", URL: "https://x/geoip.dat", Present: true, Size: 1234, UpdatedAt: 1760000000},
				{Name: "geosite.dat", URL: "https://x/geosite.dat"}}},
		{"update geo assets", testserver.OK(json.RawMessage(`{"files":[{"name":"geoip.dat","ok":true,"skipped":true},` +
			`{"name":"geosite.dat","ok":false,"error":"timeout"}]}`)),
			func(ctx context.Context) (any, error) { return c.UpdateGeoAssets(ctx, nil, throne.OnlyMissing()) },
			&throne.GeoUpdate{Files: []throne.GeoFileResult{{Name: "geoip.dat", OK: true, Skipped: true},
				{Name: "geosite.dat", Error: "timeout"}}}},
		{"update geo assets downloading", testserver.Accepted(json.RawMessage(`{"state":"downloading"}`)),
			func(ctx context.Context) (any, error) { return c.UpdateGeoAssets(ctx, nil) },
			&throne.GeoUpdate{State: throne.StateDownloading}},
		{"restart", testserver.Accepted(json.RawMessage(`{"restarting":true,"resume":true}`)),
			func(ctx context.Context) (any, error) { return c.Restart(ctx) },
			true},
		{"shutdown", testserver.Accepted(json.RawMessage(`{"stopping":true}`)),
			func(ctx context.Context) (any, error) { return nil, c.Shutdown(ctx) },
			nil},
		{"apply restore", testserver.OK(json.RawMessage(`{"restored":["profiles","routes"],"skipped_rules":2,"restarting":true}`)),
			func(ctx context.Context) (any, error) { return c.ApplyRestore(ctx, 2, nil) },
			&throne.RestoreResult{Restored: []string{"profiles", "routes"}, SkippedRules: 2, Restarting: true}},
		{"apply restore applying", testserver.Accepted(json.RawMessage(`{"id":2,"state":"applying"}`)),
			func(ctx context.Context) (any, error) { return c.ApplyRestore(ctx, 2, nil) },
			&throne.RestoreResult{State: throne.StateApplying}},
		{"restore status complete", testserver.OK(json.RawMessage(`{"id":2,"size":1500,"received":1500,"state":"complete",` +
			`"archive":{"created_at":"Thu Oct 16 15:04:05 2026","platform":"winnt","parts":["profiles","icons"]}}`)),
			func(ctx context.Context) (any, error) { return c.RestoreStatus(ctx, 2) },
			&throne.RestoreUpload{ID: 2, Size: 1500, Received: 1500, State: throne.UploadComplete, Archive: &throne.Archive{
				CreatedAt: "Thu Oct 16 15:04:05 2026", Platform: "winnt", Parts: []string{"profiles", "icons"}}}},
		{"restore status failed", testserver.OK(json.RawMessage(`{"id":2,"size":1500,"received":1500,"state":"failed",` +
			`"error":"The archive holds nothing Throne can restore"}`)),
			func(ctx context.Context) (any, error) { return c.RestoreStatus(ctx, 2) },
			&throne.RestoreUpload{ID: 2, Size: 1500, Received: 1500, State: throne.UploadFailed,
				Error: "The archive holds nothing Throne can restore"}},
		{"restore status uploading", testserver.OK(json.RawMessage(`{"id":2,"size":1500,"received":1000,"state":"uploading"}`)),
			func(ctx context.Context) (any, error) { return c.RestoreStatus(ctx, 2) },
			&throne.RestoreUpload{ID: 2, Size: 1500, Received: 1000, State: throne.UploadUploading}},
	}
	for _, tt := range tests {
		resp.Store(tt.resp)
		got, err := tt.call(t.Context())
		if err != nil {
			t.Errorf("%s: %v", tt.name, err)
			continue
		}
		if !reflect.DeepEqual(got, tt.want) {
			t.Errorf("%s = %+v, want %+v", tt.name, got, tt.want)
		}
	}

	resp.Store(testserver.Error(403, "forbidden", "Management requests are accepted only from this device."))
	if _, err := c.DeleteGroup(t.Context(), 3); throne.ErrorCode(err) != throne.CodeForbidden {
		t.Fatalf("err = %v, want forbidden", err)
	}
}

// fakeBackup serves one backup archive in chunks, as Throne does. Calls arrive one at a
// time.
type fakeBackup struct {
	archive []byte
	// building is how many reads Throne answers busy before the archive is built.
	building int
	// tamper may change a chunk on its way out.
	tamper func(offset int, data []byte, eof bool) ([]byte, bool)
	post   *testserver.Response
}

func (f *fakeBackup) answer(r *testserver.Request) *testserver.Response {
	sum := sha256.Sum256(f.archive)
	sha := hex.EncodeToString(sum[:])
	switch r.Method + " " + r.Path {
	case "POST /v1/backups":
		switch {
		case f.post != nil:
			return f.post
		case f.building > 0:
			return testserver.Accepted(map[string]any{"id": 4, "state": "building"})
		}
		return testserver.OK(map[string]any{"id": 4, "size": len(f.archive), "sha256": sha, "parts": backupParts})
	case "GET /v1/backups/4":
		if f.building > 0 {
			f.building--
			return testserver.Error(409, "busy", "The backup is still being built")
		}
		offset, err1 := strconv.Atoi(r.Query["offset"])
		length, err2 := strconv.Atoi(r.Query["length"])
		if err1 != nil || err2 != nil || offset > len(f.archive) || length <= 0 {
			return testserver.Error(400, "bad_request", "Bad range")
		}
		end := min(offset+length, len(f.archive))
		data, eof := f.archive[offset:end], end == len(f.archive)
		if f.tamper != nil {
			data, eof = f.tamper(offset, bytes.Clone(data), eof)
		}
		return testserver.OK(map[string]any{"id": 4, "size": len(f.archive), "sha256": sha, "parts": backupParts,
			"offset": offset, "data": data, "eof": eof})
	case "DELETE /v1/backups/4":
		return testserver.OK(map[string]any{"deleted": 4})
	}
	return testserver.Error(404, "not_found", "Unknown path")
}

// backupParts are the parts of the archive that fakeBackup serves.
var backupParts = []string{"profiles", "otp"}

// archiveBytes returns n bytes that differ from chunk to chunk.
func archiveBytes(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(i*7 + i/251)
	}
	return b
}

type writerFunc func([]byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) { return f(p) }

// paths lists the requests as "METHOD path?offset".
func paths(reqs []*testserver.Request) []string {
	var out []string
	for _, r := range reqs {
		s := r.Method + " " + r.Path
		if off, ok := r.Query["offset"]; ok {
			s += "?" + off + "+" + r.Query["length"]
		}
		out = append(out, s)
	}
	return out
}

func TestBackup(t *testing.T) {
	defer throne.SetPollInterval(10 * time.Millisecond)()
	defer throne.SetBackupChunk(4096)()
	archive := archiveBytes(10000)
	sum := sha256.Sum256(archive)
	sha := hex.EncodeToString(sum[:])

	t.Run("chunks", func(t *testing.T) {
		f := &fakeBackup{archive: archive}
		rec := &recorder{answer: f.answer}
		c, _ := serve(t, rec.handle)
		var buf bytes.Buffer
		var raw json.RawMessage
		info, err := c.Backup(rawbody.With(t.Context(), &raw), &buf, []string{throne.PartProfiles, throne.PartOTP})
		if err != nil {
			t.Fatal(err)
		}
		want := &throne.BackupInfo{ID: 4, Size: 10000, SHA256: sha, Parts: []string{"profiles", "otp"}}
		if !reflect.DeepEqual(info, want) {
			t.Fatalf("BackupInfo = %+v, want %+v", info, want)
		}
		if !bytes.Equal(buf.Bytes(), archive) {
			t.Fatal("the written archive differs")
		}
		reqs := rec.requests()
		wantPaths := []string{"POST /v1/backups", "GET /v1/backups/4?0+4096", "GET /v1/backups/4?4096+4096",
			"GET /v1/backups/4?8192+4096", "DELETE /v1/backups/4"}
		if got := paths(reqs); !reflect.DeepEqual(got, wantPaths) {
			t.Fatalf("requests %v, want %v", got, wantPaths)
		}
		jsonEqual(t, "POST body", reqs[0].Body, `{"parts":["profiles","otp"]}`)
		jsonEqual(t, "raw body", raw, `{"id":4,"size":10000,"sha256":"`+sha+`","parts":["profiles","otp"]}`)
	})

	t.Run("building", func(t *testing.T) {
		f := &fakeBackup{archive: archive[:3000], building: 2}
		rec := &recorder{answer: f.answer}
		c, _ := serve(t, rec.handle)
		var buf bytes.Buffer
		info, err := c.Backup(t.Context(), &buf, nil, throne.NoWait())
		if err != nil {
			t.Fatal(err)
		}
		s := sha256.Sum256(archive[:3000])
		want := &throne.BackupInfo{ID: 4, Size: 3000, SHA256: hex.EncodeToString(s[:]), Parts: backupParts}
		if !reflect.DeepEqual(info, want) || !bytes.Equal(buf.Bytes(), archive[:3000]) {
			t.Fatalf("BackupInfo = %+v, %d bytes", info, buf.Len())
		}
		reqs := rec.requests()
		wantPaths := []string{"POST /v1/backups", "GET /v1/backups/4?0+4096", "GET /v1/backups/4?0+4096",
			"GET /v1/backups/4?0+4096", "DELETE /v1/backups/4"}
		if got := paths(reqs); !reflect.DeepEqual(got, wantPaths) {
			t.Fatalf("requests %v, want %v", got, wantPaths)
		}
		jsonEqual(t, "POST body", reqs[0].Body, `{"wait":false}`)
	})

	failures := []struct {
		name   string
		tamper func(offset int, data []byte, eof bool) ([]byte, bool)
		want   string
	}{
		{"corrupt", func(offset int, data []byte, eof bool) ([]byte, bool) {
			if offset == 4096 {
				data[10] ^= 1
			}
			return data, eof
		}, "SHA-256"},
		{"short", func(offset int, data []byte, eof bool) ([]byte, bool) {
			return data, eof || offset == 4096
		}, "ended after 8192 of 10000 bytes"},
		{"stalled", func(offset int, data []byte, eof bool) ([]byte, bool) {
			if offset == 4096 {
				return nil, false
			}
			return data, eof
		}, "empty backup chunk"},
	}
	for _, tt := range failures {
		t.Run(tt.name, func(t *testing.T) {
			f := &fakeBackup{archive: archive, tamper: tt.tamper}
			rec := &recorder{answer: f.answer}
			c, _ := serve(t, rec.handle)
			_, err := c.Backup(t.Context(), io.Discard, nil)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want %q", err, tt.want)
			}
			if r := rec.last(t); r.Method != "DELETE" || r.Path != "/v1/backups/4" {
				t.Fatalf("last request %s %s, want the delete", r.Method, r.Path)
			}
		})
	}

	t.Run("cancelled", func(t *testing.T) {
		f := &fakeBackup{archive: archive}
		rec := &recorder{answer: f.answer}
		c, _ := serve(t, rec.handle)
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		w := writerFunc(func(p []byte) (int, error) { cancel(); return len(p), nil })
		if _, err := c.Backup(ctx, w, nil); !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want context.Canceled", err)
		}
		if r := rec.last(t); r.Method != "DELETE" || r.Path != "/v1/backups/4" {
			t.Fatalf("last request %s %s, want the delete", r.Method, r.Path)
		}
	})

	t.Run("write error", func(t *testing.T) {
		f := &fakeBackup{archive: archive}
		rec := &recorder{answer: f.answer}
		c, _ := serve(t, rec.handle)
		disk := errors.New("disk full")
		w := writerFunc(func([]byte) (int, error) { return 0, disk })
		if _, err := c.Backup(t.Context(), w, nil); !errors.Is(err, disk) {
			t.Fatalf("err = %v, want the write error", err)
		}
		if got := paths(rec.requests()); len(got) != 3 || got[2] != "DELETE /v1/backups/4" {
			t.Fatalf("requests %v", got)
		}
	})

	t.Run("refused", func(t *testing.T) {
		f := &fakeBackup{archive: archive, post: testserver.Error(409, "busy", "Two backups are kept already")}
		rec := &recorder{answer: f.answer}
		c, _ := serve(t, rec.handle)
		if _, err := c.Backup(t.Context(), io.Discard, nil); throne.ErrorCode(err) != throne.CodeBusy {
			t.Fatalf("err = %v, want busy", err)
		}
		if n := len(rec.requests()); n != 1 {
			t.Fatalf("%d requests, want only the refused one", n)
		}
	})
}

// fakeRestore takes one upload in chunks, as Throne does. Calls arrive one at a time.
type fakeRestore struct {
	limit    int
	size     int64
	sha      string
	received []byte
	// complete, when set, answers the chunk that completes the upload.
	complete *testserver.Response
	// accept makes the completing chunk answer 202, as when Throne's wait ends during the
	// check; inspecting is how many reads of the upload still find it being checked.
	accept     bool
	inspecting int
	// refuse is why the check refuses the archive, "" to accept it.
	refuse string
}

// fakeArchive is how fakeRestore describes the archive it checked.
var fakeArchive = map[string]any{"created_at": "Thu Oct 16 15:04:05 2026", "platform": "linux", "parts": []string{"profiles", "routes"}}

func (f *fakeRestore) status() map[string]any {
	st := map[string]any{"id": 2, "size": f.size, "received": len(f.received), "state": "uploading"}
	switch {
	case int64(len(f.received)) < f.size:
	case f.inspecting > 0:
		f.inspecting--
		st["state"] = "inspecting"
	case f.refuse != "":
		st["state"], st["error"] = "failed", f.refuse
	default:
		st["state"], st["archive"] = "complete", fakeArchive
	}
	return st
}

func (f *fakeRestore) answer(r *testserver.Request) *testserver.Response {
	switch r.Method + " " + r.Path {
	case "POST /v1/restores":
		var b struct {
			Size   int64  `json:"size"`
			SHA256 string `json:"sha256"`
		}
		if err := json.Unmarshal(r.Body, &b); err != nil {
			return testserver.Error(400, "bad_request", err.Error())
		}
		f.size, f.sha = b.Size, b.SHA256
		return testserver.OK(map[string]any{"id": 2, "chunk_limit": f.limit})
	case "PUT /v1/restores/2":
		var b struct {
			Offset int64  `json:"offset"`
			Data   []byte `json:"data"`
		}
		if err := json.Unmarshal(r.Body, &b); err != nil {
			return testserver.Error(400, "bad_request", err.Error())
		}
		if b.Offset != int64(len(f.received)) {
			return testserver.Error(409, "bad_offset", fmt.Sprintf("Expected offset %d", len(f.received)))
		}
		if limit := f.limit; (limit > 0 && len(b.Data) > limit) || len(b.Data) > 512<<10 {
			return testserver.Error(400, "bad_request", "Chunk too large")
		}
		f.received = append(f.received, b.Data...)
		resp := map[string]any{"id": 2, "size": f.size, "received": len(f.received)}
		if int64(len(f.received)) == f.size {
			if f.complete != nil {
				return f.complete
			}
			if sum := sha256.Sum256(f.received); hex.EncodeToString(sum[:]) != f.sha {
				return testserver.Error(422, "checksum_mismatch", "The archive does not match its SHA-256")
			}
			if f.accept {
				return testserver.Accepted(map[string]any{"id": 2, "state": "inspecting"})
			}
			if f.refuse != "" {
				return testserver.Error(422, "invalid_archive", f.refuse)
			}
			resp["archive"] = fakeArchive
		}
		return testserver.OK(resp)
	case "GET /v1/restores/2":
		return testserver.OK(f.status())
	case "DELETE /v1/restores/2":
		return testserver.OK(map[string]any{"deleted": 2})
	}
	return testserver.Error(404, "not_found", "Unknown path")
}

// chunks returns the offset and size of each uploaded chunk, and checks that a chunk
// without a timeout has no other fields.
func chunks(t *testing.T, reqs []*testserver.Request) (offsets, sizes []int) {
	t.Helper()
	puts := 0
	for _, r := range reqs {
		if r.Method != "PUT" {
			continue
		}
		puts++
		var b struct {
			Offset  int    `json:"offset"`
			Data    []byte `json:"data"`
			Timeout *int   `json:"timeout"`
		}
		if err := json.Unmarshal(r.Body, &b); err != nil {
			t.Fatal(err)
		}
		offsets, sizes = append(offsets, b.Offset), append(sizes, len(b.Data))
		if n := len(r.Fields()); n != 2 && b.Timeout == nil {
			t.Errorf("chunk at %d: body has %d fields", b.Offset, n)
		}
	}
	return offsets, sizes
}

func TestUploadRestore(t *testing.T) {
	t.Run("chunks", func(t *testing.T) {
		archive := archiveBytes(2500)
		f := &fakeRestore{limit: 1000}
		rec := &recorder{answer: f.answer}
		c, _ := serve(t, rec.handle)
		ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
		defer cancel()
		up, err := c.UploadRestore(ctx, bytes.NewReader(archive))
		if err != nil {
			t.Fatal(err)
		}
		want := &throne.RestoreUpload{ID: 2, Size: 2500, Received: 2500, State: throne.UploadComplete,
			Archive: &throne.Archive{CreatedAt: "Thu Oct 16 15:04:05 2026", Platform: "linux", Parts: []string{"profiles", "routes"}}}
		if !reflect.DeepEqual(up, want) {
			t.Fatalf("RestoreUpload = %+v, want %+v", up, want)
		}
		if !bytes.Equal(f.received, archive) {
			t.Fatal("the uploaded archive differs")
		}
		reqs := rec.requests()
		sum := sha256.Sum256(archive)
		jsonEqual(t, "POST body", reqs[0].Body, `{"size":2500,"sha256":"`+hex.EncodeToString(sum[:])+`"}`)
		offsets, sizes := chunks(t, reqs)
		if !reflect.DeepEqual(offsets, []int{0, 1000, 2000}) || !reflect.DeepEqual(sizes, []int{1000, 1000, 500}) {
			t.Fatalf("chunks at %v of sizes %v", offsets, sizes)
		}
		for i, r := range reqs[1:] {
			to, _ := r.Fields()["timeout"].(float64)
			if last := i == len(reqs)-2; last != (to >= 16 && to <= 18) {
				t.Errorf("chunk %d: body has timeout %v", i, r.Fields()["timeout"])
			}
		}
	})

	sizes := []struct {
		name  string
		limit int
		size  int
		want  []int
	}{
		{"limit above 512 KiB", 4 << 20, 1200 << 10, []int{512 << 10, 512 << 10, 176 << 10}},
		{"no limit", 0, 600 << 10, []int{512 << 10, 88 << 10}},
		{"limit of 1 byte", 1, 3, []int{1, 1, 1}},
	}
	for _, tt := range sizes {
		t.Run(tt.name, func(t *testing.T) {
			archive := archiveBytes(tt.size)
			f := &fakeRestore{limit: tt.limit}
			rec := &recorder{answer: f.answer}
			c, _ := serve(t, rec.handle)
			if _, err := c.UploadRestore(t.Context(), bytes.NewReader(archive)); err != nil {
				t.Fatal(err)
			}
			if _, got := chunks(t, rec.requests()); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("chunk sizes %v, want %v", got, tt.want)
			}
			if !bytes.Equal(f.received, archive) {
				t.Fatal("the uploaded archive differs")
			}
		})
	}

	t.Run("from the current offset", func(t *testing.T) {
		archive := archiveBytes(1500)
		r := bytes.NewReader(append([]byte("prefix"), archive...))
		if _, err := r.Seek(6, io.SeekStart); err != nil {
			t.Fatal(err)
		}
		f := &fakeRestore{limit: 1000}
		rec := &recorder{answer: f.answer}
		c, _ := serve(t, rec.handle)
		up, err := c.UploadRestore(t.Context(), r)
		if err != nil {
			t.Fatal(err)
		}
		if up.Size != 1500 || up.Archive == nil || !bytes.Equal(f.received, archive) {
			t.Fatalf("RestoreUpload = %+v", up)
		}
	})

	t.Run("checksum mismatch", func(t *testing.T) {
		f := &fakeRestore{limit: 1000, complete: testserver.Error(422, "checksum_mismatch", "The archive does not match its SHA-256")}
		rec := &recorder{answer: f.answer}
		c, _ := serve(t, rec.handle)
		if _, err := c.UploadRestore(t.Context(), bytes.NewReader(archiveBytes(1500))); throne.ErrorCode(err) != throne.CodeChecksumMismatch {
			t.Fatalf("err = %v, want checksum_mismatch", err)
		}
		if r := rec.last(t); r.Method != "DELETE" || r.Path != "/v1/restores/2" {
			t.Fatalf("last request %s %s, want the delete", r.Method, r.Path)
		}
	})

	t.Run("refused", func(t *testing.T) {
		f := &fakeRestore{limit: 1000, refuse: "The archive holds nothing Throne can restore"}
		rec := &recorder{answer: f.answer}
		c, _ := serve(t, rec.handle)
		_, err := c.UploadRestore(t.Context(), bytes.NewReader(archiveBytes(1500)))
		var apiErr *throne.APIError
		if !errors.As(err, &apiErr) || apiErr.Code != throne.CodeInvalidArchive || apiErr.Message != f.refuse {
			t.Fatalf("err = %v, want invalid_archive", err)
		}
		if r := rec.last(t); r.Method != "DELETE" || r.Path != "/v1/restores/2" {
			t.Fatalf("last request %s %s, want the delete", r.Method, r.Path)
		}
	})

	defer throne.SetPollInterval(10 * time.Millisecond)()
	uploaded := []string{"POST /v1/restores", "PUT /v1/restores/2", "PUT /v1/restores/2"}

	t.Run("checked after the wait", func(t *testing.T) {
		f := &fakeRestore{limit: 1000, accept: true, inspecting: 2}
		rec := &recorder{answer: f.answer}
		c, _ := serve(t, rec.handle)
		up, err := c.UploadRestore(t.Context(), bytes.NewReader(archiveBytes(1500)))
		if err != nil {
			t.Fatal(err)
		}
		want := &throne.RestoreUpload{ID: 2, Size: 1500, Received: 1500, State: throne.UploadComplete,
			Archive: &throne.Archive{CreatedAt: "Thu Oct 16 15:04:05 2026", Platform: "linux", Parts: []string{"profiles", "routes"}}}
		if !reflect.DeepEqual(up, want) {
			t.Fatalf("RestoreUpload = %+v, want %+v", up, want)
		}
		wantPaths := append(slices.Clone(uploaded), "GET /v1/restores/2", "GET /v1/restores/2", "GET /v1/restores/2")
		if got := paths(rec.requests()); !reflect.DeepEqual(got, wantPaths) {
			t.Fatalf("requests %v, want %v", got, wantPaths)
		}
	})

	t.Run("refused after the wait", func(t *testing.T) {
		f := &fakeRestore{limit: 1000, accept: true, inspecting: 1, refuse: "The archive holds nothing Throne can restore"}
		rec := &recorder{answer: f.answer}
		c, _ := serve(t, rec.handle)
		_, err := c.UploadRestore(t.Context(), bytes.NewReader(archiveBytes(1500)))
		var apiErr *throne.APIError
		if !errors.As(err, &apiErr) || apiErr.Status != 422 || apiErr.Code != throne.CodeInvalidArchive || apiErr.Message != f.refuse {
			t.Fatalf("err = %v, want invalid_archive", err)
		}
		wantPaths := append(slices.Clone(uploaded), "GET /v1/restores/2", "GET /v1/restores/2", "DELETE /v1/restores/2")
		if got := paths(rec.requests()); !reflect.DeepEqual(got, wantPaths) {
			t.Fatalf("requests %v, want %v", got, wantPaths)
		}
	})

	t.Run("check outlasts ctx", func(t *testing.T) {
		f := &fakeRestore{limit: 1000, accept: true, inspecting: 1 << 30}
		rec := &recorder{answer: f.answer}
		c, _ := serve(t, rec.handle)
		ctx, cancel := context.WithTimeout(t.Context(), 300*time.Millisecond)
		defer cancel()
		if _, err := c.UploadRestore(ctx, bytes.NewReader(archiveBytes(1500))); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("err = %v, want context.DeadlineExceeded", err)
		}
		if r := rec.last(t); r.Method != "DELETE" || r.Path != "/v1/restores/2" {
			t.Fatalf("last request %s %s, want the delete", r.Method, r.Path)
		}
	})

	t.Run("lost upload", func(t *testing.T) {
		f := &fakeRestore{limit: 1000, accept: true}
		rec := &recorder{answer: func(r *testserver.Request) *testserver.Response {
			if r.Method == "GET" {
				return testserver.Error(404, "not_found", "Unknown restore 2")
			}
			return f.answer(r)
		}}
		c, _ := serve(t, rec.handle)
		if _, err := c.UploadRestore(t.Context(), bytes.NewReader(archiveBytes(1500))); throne.ErrorCode(err) != throne.CodeNotFound {
			t.Fatalf("err = %v, want not_found", err)
		}
	})

	t.Run("empty", func(t *testing.T) {
		rec := &recorder{answer: (&fakeRestore{}).answer}
		c, _ := serve(t, rec.handle)
		if _, err := c.UploadRestore(t.Context(), bytes.NewReader(nil)); err == nil {
			t.Fatal("an empty archive was accepted")
		}
		if n := len(rec.requests()); n != 0 {
			t.Fatalf("%d requests for an empty archive", n)
		}
	})
}

func TestLogs(t *testing.T) {
	var resp atomic.Pointer[testserver.Response]
	resp.Store(testserver.OK(json.RawMessage(`{"instance":"9f2c01d4a7be3355","cursor":1452,"more":true,"dropped":3,` +
		`"lines":[{"seq":1451,"time":1760000000,"text":"[Core] started"},{"seq":1452,"time":1760000001,"text":"…"}]}`)))
	rec := &recorder{answer: func(*testserver.Request) *testserver.Response { return resp.Load() }}
	c, _ := serve(t, rec.handle)
	tests := []struct {
		q     throne.LogQuery
		query map[string]string
		wait  time.Duration
	}{
		{throne.LogQuery{}, nil, 0},
		{throne.LogQuery{Limit: 50}, map[string]string{"limit": "50"}, 0},
		{throne.LogQuery{Limit: 5000}, map[string]string{"limit": "1000"}, 0},
		{throne.LogQuery{Limit: -1}, nil, 0},
		{throne.LogQuery{Wait: 25 * time.Second, After: 7}, nil, 0},
		{throne.LogQuery{Instance: "9f2c01d4a7be3355", After: 1452, Limit: 1000, Wait: 25 * time.Second},
			map[string]string{"instance": "9f2c01d4a7be3355", "after": "1452", "limit": "1000", "timeout": "25"}, 25 * time.Second},
		{throne.LogQuery{Instance: "a", Wait: 400 * time.Millisecond}, map[string]string{"instance": "a", "after": "0"}, 0},
		{throne.LogQuery{Instance: "a", After: 2, Wait: 2*time.Second + 900*time.Millisecond},
			map[string]string{"instance": "a", "after": "2", "timeout": "2"}, 2 * time.Second},
		{throne.LogQuery{Instance: "a", After: 2, Wait: 301 * time.Second},
			map[string]string{"instance": "a", "after": "2", "timeout": "300"}, 300 * time.Second},
	}
	want := &throne.Logs{Instance: "9f2c01d4a7be3355", Cursor: 1452, More: true, Dropped: 3, Lines: []throne.LogLine{
		{Seq: 1451, Time: 1760000000, Text: "[Core] started"}, {Seq: 1452, Time: 1760000001, Text: "…"}}}
	for _, tt := range tests {
		logs, err := c.Logs(t.Context(), tt.q)
		if err != nil {
			t.Fatalf("%+v: %v", tt.q, err)
		}
		if !reflect.DeepEqual(logs, want) {
			t.Errorf("%+v: Logs = %+v", tt.q, logs)
		}
		if r := rec.last(t); r.Method != "GET" || r.Path != "/v1/logs" || !reflect.DeepEqual(r.Query, tt.query) || r.Body != nil {
			t.Errorf("%+v: request %s %s %v %s, want the query %v", tt.q, r.Method, r.Path, r.Query, r.Body, tt.query)
		}
		if w := throne.LogsWait(tt.q); w != tt.wait {
			t.Errorf("%+v: the call allows a hold of %v, want %v", tt.q, w, tt.wait)
		}
	}

	resp.Store(testserver.OK(json.RawMessage(`{"instance":"b","cursor":0,"more":false,"dropped":0,"lines":[]}`)))
	if logs, err := c.Logs(t.Context(), throne.LogQuery{}); err != nil || logs.Instance != "b" || logs.Lines == nil || len(logs.Lines) != 0 {
		t.Fatalf("empty log: %+v, %v", logs, err)
	}
	resp.Store(testserver.Error(403, "forbidden", "Management requests are accepted only from this device."))
	if _, err := c.Logs(t.Context(), throne.LogQuery{}); throne.ErrorCode(err) != throne.CodeForbidden {
		t.Fatalf("err = %v, want forbidden", err)
	}
}
