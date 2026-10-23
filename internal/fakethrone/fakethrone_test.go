package fakethrone_test

import (
	"bytes"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	throne "github.com/throneproj/Throne-api"
	"github.com/throneproj/Throne-api/internal/fakethrone"
)

// A key from spec/vectors/v1.json.
const testKey = "AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8"

func TestFake(t *testing.T) {
	s, err := fakethrone.Start("127.0.0.1:0", fakethrone.Config{Key: testKey, Seed: 7})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	key, err := throne.ParseKey(testKey)
	if err != nil {
		t.Fatal(err)
	}
	c := throne.New(s.Addr(), key)
	ctx := t.Context()

	st, err := c.Status(ctx)
	if err != nil || st.State != throne.StateRunning || st.Profile == nil || !st.Capabilities.Headless {
		t.Fatalf("Status = %+v, %v", st, err)
	}
	ev, err := c.Events(ctx, throne.EventsQuery{})
	all := []string{throne.TopicStatus, throne.TopicGroups, throne.TopicProfiles, throne.TopicTests, throne.TopicSettings}
	if err != nil || len(ev.Instance) != 16 || !reflect.DeepEqual(ev.Changes, all) {
		t.Fatalf("Events = %+v, %v", ev, err)
	}
	logs, err := c.Logs(ctx, throne.LogQuery{Limit: 2})
	if err != nil || logs.Instance != ev.Instance || len(logs.Lines) != 2 || logs.Lines[1].Seq != logs.Cursor {
		t.Fatalf("Logs = %+v, %v", logs, err)
	}

	go func() {
		time.Sleep(300 * time.Millisecond)
		if _, err := c.Disconnect(ctx, throne.NoWait()); err != nil {
			t.Errorf("Disconnect: %v", err)
		}
	}()
	held, err := c.Events(ctx, throne.EventsQuery{Instance: ev.Instance, After: ev.Cursor, Wait: 10 * time.Second})
	if err != nil || held.Cursor <= ev.Cursor || !slices.Contains(held.Changes, throne.TopicStatus) {
		t.Fatalf("held Events = %+v, %v", held, err)
	}
	found := false
	for q := (throne.LogQuery{Instance: logs.Instance, After: logs.Cursor, Wait: 10 * time.Second}); !found; {
		next, err := c.Logs(ctx, q)
		if err != nil || next.Instance != logs.Instance {
			t.Fatalf("held Logs = %+v, %v", next, err)
		}
		for _, l := range next.Lines {
			found = found || strings.Contains(l.Text, "DELETE /v1/connection")
		}
		q.After = next.Cursor
	}

	on := true
	if _, err := c.SetModes(ctx, throne.Modes{SystemProxy: &on}); throne.ErrorCode(err) != throne.CodeSystemProxyUnsupported {
		t.Errorf("system proxy on: %v", err)
	}
	res, err := c.Import(ctx, throne.ImportSource{Text: "vless://id@a.example.com:443#A\ntrojan://pw@b.example.com\njunk"})
	if err != nil || res.Imported != 2 || res.Failed != 1 || res.Warned != 1 || len(res.Issues) != 2 {
		t.Errorf("Import = %+v, %v", res, err)
	}

	var archive bytes.Buffer
	info, err := c.Backup(ctx, &archive, []string{throne.PartProfiles, throne.PartSettings})
	if err != nil || !bytes.HasPrefix(archive.Bytes(), []byte("THRN")) || !slices.Equal(info.Parts, []string{"profiles", "settings"}) {
		t.Fatalf("Backup = %+v, %v", info, err)
	}
	up, err := c.UploadRestore(ctx, bytes.NewReader(archive.Bytes()))
	if err != nil || up.State != throne.UploadComplete || !slices.Equal(up.Archive.Parts, info.Parts) {
		t.Fatalf("UploadRestore = %+v, %v", up, err)
	}
	if err := c.CancelRestore(ctx, up.ID); err != nil {
		t.Fatal(err)
	}
}
