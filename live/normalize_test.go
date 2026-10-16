package live

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	throne "github.com/throneproj/Throne-api"
)

func statusJSON(latency, at, up, down int, version string) string {
	return fmt.Sprintf(`{"state":"running","profile":{"id":12,"group":3,"name":"DE","type":"vless","address":"de1:443",`+
		`"latency_ms":%d,"latency_at":%d,"download_bps":0,"upload_bps":0,"country":"DE","ip_out":"",`+
		`"traffic_up":%d,"traffic_down":%d,"running":true},"last_profile_id":12,"tun":false,"system_proxy":true,`+
		`"route":{"id":1,"name":"Default","active":true},"kill_switch":"off","core":true,`+
		`"inbound":{"enabled":true,"address":"127.0.0.1","port":2080},"version":%q}`, latency, at, up, down, version)
}

// compact marshals v as the transcripts do, without HTML escaping.
func compact(v any) string {
	var b strings.Builder
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		panic(err)
	}
	return strings.TrimSuffix(b.String(), "\n")
}

func TestNormalizeMasksRunDependentValues(t *testing.T) {
	a := normalize([]byte(statusJSON(85, 1760000000, 1024, 4096, "1.4.0")), nil)
	b := normalize([]byte(statusJSON(-1, 1760000999, 7, 9, "1.4.1")), nil)
	if !reflect.DeepEqual(a, b) {
		t.Fatalf("runs differ after normalizing:\n%v\n%v", a, b)
	}
	p := a.(map[string]any)["profile"].(map[string]any)
	if p["id"] != json.Number("12") || p["running"] != true || p["latency_ms"] != masked || p["traffic_down"] != masked {
		t.Errorf("profile = %v", p)
	}
	if a.(map[string]any)["route"].(map[string]any)["id"] != json.Number("1") {
		t.Errorf("route id was masked")
	}
}

func TestNormalizeMasksJobIDOnly(t *testing.T) {
	v := normalize([]byte(`{"id":7,"kind":"latency","state":"done","profile_ids":[12,15],"started_at":1,"finished_at":2,`+
		`"results":[{"id":12,"latency_ms":85}]}`), nil)
	got := compact(v)
	want := `{"finished_at":"<masked>","id":"<masked>","kind":"latency","profile_ids":[12,15],` +
		`"results":[{"id":12,"latency_ms":"<masked>"}],"started_at":"<masked>","state":"done"}`
	if got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
}

func TestNormalizePath(t *testing.T) {
	for in, want := range map[string]string{
		"/v1/tests/42":     "/v1/tests/{job}",
		"/v1/tests":        "/v1/tests",
		"/v1/profiles/42":  "/v1/profiles/42",
		"/v1/tests/42/foo": "/v1/tests/42/foo",
	} {
		if got := normalizePath(in); got != want {
			t.Errorf("normalizePath(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestViews(t *testing.T) {
	list := []byte(`{"profiles":[{"id":3},{"id":12},{"id":0}]}`)
	got := compact(normalize(list, idsView([]int{12, 15, 0})))
	if got != `{"includes":[12,0]}` {
		t.Errorf("idsView: %s", got)
	}
	jobs := []byte(`{"jobs":[{"id":9,"kind":"ip","state":"running","profile_ids":[1]},{"id":8,"kind":"latency","state":"done","profile_ids":[2]}]}`)
	got = compact(normalize(jobs, jobView(8)))
	if got != `{"job":{"id":"<masked>","kind":"latency","profile_ids":[2],"state":"done"}}` {
		t.Errorf("jobView: %s", got)
	}
	for _, state := range []string{"cancelled", "done"} {
		job := []byte(`{"id":5,"kind":"latency","state":"` + state + `","profile_ids":[1,2,3],"results":[{"id":1}]}`)
		got = compact(normalize(job, cancelView))
		if got != `{"id":"<masked>","kind":"latency","profile_ids":"<masked>","results":"<masked>","state":"cancelled|done"}` {
			t.Errorf("cancelView(%s): %s", state, got)
		}
	}
}

type fakeCaller func(method, path string, body any) (string, error)

func (f fakeCaller) Call(_ context.Context, method, path string, _ map[string]string, body, out any) error {
	resp, err := f(method, path, body)
	if err != nil {
		return err
	}
	return json.Unmarshal([]byte(resp), out)
}

func TestRecorderTranscript(t *testing.T) {
	c := fakeCaller(func(method, path string, body any) (string, error) {
		if method == "DELETE" {
			return "", &throne.APIError{Status: 409, Code: throne.CodeBusy, Message: "translated text"}
		}
		return `{"id":31,"kind":"latency","state":"running","profile_ids":[4],"started_at":1760000000,"finished_at":0}`, nil
	})
	var r recorder
	for _, x := range []exchange{
		{step: "start", method: "POST", path: "/v1/tests", body: map[string]any{"kind": "latency", "profiles": []int{4}}},
		{step: "cancel", method: "DELETE", path: "/v1/tests/31"},
	} {
		raw, err := send(context.Background(), c, x)
		r.record(x, raw, err)
	}
	r.note("check", true)
	dir := t.TempDir()
	if err := r.write(dir, "01-x.json", "x", false); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(dir, "01-x.json"))
	if err != nil {
		t.Fatal(err)
	}
	want := `{
  "subtest": "x",
  "entries": [
    {
      "step": "start",
      "request": {
        "method": "POST",
        "path": "/v1/tests",
        "body": {
          "kind": "latency",
          "profiles": [
            4
          ]
        }
      },
      "response": {
        "finished_at": "<masked>",
        "id": "<masked>",
        "kind": "latency",
        "profile_ids": [
          4
        ],
        "started_at": "<masked>",
        "state": "running"
      }
    },
    {
      "step": "cancel",
      "request": {
        "method": "DELETE",
        "path": "/v1/tests/{job}"
      },
      "error": {
        "status": 409,
        "code": "busy"
      }
    },
    {
      "step": "check",
      "value": true
    }
  ]
}
`
	if string(got) != want {
		t.Errorf("transcript:\n%s\nwant:\n%s", got, want)
	}
}
