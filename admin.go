package throne

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/throneproj/Throne-api/internal/rawbody"
)

// Throne's default timeout field of the management requests that wait.
const (
	importWait       = 60 * time.Second
	groupUpdateWait  = 60 * time.Second
	groupsUpdateWait = 120 * time.Second
	deleteWait       = 30 * time.Second
	ruleSetWait      = 90 * time.Second
	geoWait          = 300 * time.Second
	backupWait       = 120 * time.Second
	uploadWait       = 120 * time.Second
	applyWait        = 60 * time.Second
	// warpWait is how long Throne may take to register a WARP device.
	warpWait = 90 * time.Second
	// discardWait bounds the delete of a backup or an upload after a failure.
	discardWait = 10 * time.Second
	// maxUploadChunk is the most archive bytes UploadRestore sends in one request; in
	// base64 they stay well within a client frame.
	maxUploadChunk = 512 << 10
	// maxLogLines is the largest limit Throne takes for the lines of its log.
	maxLogLines = 1000
)

// backupChunk is how many archive bytes Backup asks for in one request.
var backupChunk = 4 << 20

// ImportSource is what Import imports: Text or URL, not both.
type ImportSource struct {
	// Text is imported as it is: share links, a subscription body, a Clash, sing-box or
	// Xray configuration, or a WireGuard or OpenVPN file. A line that holds only an http
	// or https URL is an HTTP proxy profile.
	Text string
	// URL is downloaded once, over http or https, and what it returns is imported.
	URL string
	// Group is the group to import into. Nil means the group Throne shows.
	Group *int
}

// Import imports profiles into a group (POST /v1/import). It is an admin request.
//
// Unless NoWait is given, Throne responds once the import has finished. If the wait ends
// first, Import returns the ImportResult in state StateQueued without an error, and the
// import carries on in Throne. Import fails with CodeNothingImported when it imported
// nothing, and with CodeFetchFailed when URL could not be downloaded.
func (a api) Import(ctx context.Context, src ImportSource, opts ...Option) (*ImportResult, error) {
	body := map[string]any{}
	switch {
	case src.Text != "" && src.URL != "":
		return nil, errors.New("throne: Import takes Text or URL, not both")
	case src.URL != "":
		body["as"], body["url"] = "url", src.URL
	default:
		body["as"], body["text"] = "profiles", src.Text
	}
	if src.Group != nil {
		body["group"] = *src.Group
	}
	return fetch[ImportResult](ctx, a, waiting(ctx, "POST", "/v1/import", body, collect(opts), importWait))
}

// NewGroup describes a group for AddGroup.
type NewGroup struct {
	// Name is the group's name; it must not be empty.
	Name string
	// URL makes a subscription group that Throne downloads from this http or https URL.
	// "" makes a plain group.
	URL string
	// SkipAutoUpdate leaves the subscription out of Throne's automatic updates.
	SkipAutoUpdate bool
	// SkipUpdate adds the subscription without downloading it now.
	SkipUpdate bool
	// RouteID gives the group's profiles their own routing profile; nil follows the active one.
	RouteID *int
	// ImportRoute keeps a routing profile from the one the subscription provides, refreshed on
	// every update. Subscription groups only.
	ImportRoute bool
}

// AddGroup adds a group at the end of the tab order (POST /v1/groups). It is an admin
// request.
//
// A subscription group is downloaded at once unless SkipUpdate is set, and AddGroup waits
// for that like UpdateGroup. A failed download is no error, as the group exists: Update
// says why it failed.
func (a api) AddGroup(ctx context.Context, g NewGroup, opts ...Option) (*GroupResult, error) {
	body := map[string]any{"name": g.Name}
	if g.URL != "" {
		body["url"] = g.URL
	}
	if g.SkipAutoUpdate {
		body["auto_update"] = false
	}
	if g.SkipUpdate {
		body["update"] = false
	}
	if g.RouteID != nil {
		body["route_id"] = *g.RouteID
	}
	if g.ImportRoute {
		body["import_route"] = true
	}
	return fetch[GroupResult](ctx, a, waiting(ctx, "POST", "/v1/groups", body, collect(opts), groupUpdateWait))
}

// UpdateGroup updates a subscription group (POST /v1/groups/{id}/update) and returns the
// group with the outcome. A failed update returns CodeFetchFailed or CodeNothingImported.
//
// Unless NoWait is given, Throne responds once the update has finished. If the wait ends
// first, Update is nil, without an error, and the update carries on in Throne.
func (a api) UpdateGroup(ctx context.Context, id int, opts ...Option) (*GroupResult, error) {
	path := "/v1/groups/" + strconv.Itoa(id) + "/update"
	return fetch[GroupResult](ctx, a, waiting(ctx, "POST", path, map[string]any{}, collect(opts), groupUpdateWait))
}

// UpdateGroups updates every subscription group that is not archived, in tab order
// (POST /v1/groups/update). A failed update is a result, not an error. It waits like
// UpdateGroup; if the wait ends first, Results is nil and Queued lists the groups being
// updated.
func (a api) UpdateGroups(ctx context.Context, opts ...Option) (*UpdateGroupsResult, error) {
	return fetch[UpdateGroupsResult](ctx, a, waiting(ctx, "POST", "/v1/groups/update", map[string]any{}, collect(opts), groupsUpdateWait))
}

// DeleteGroup deletes a group and its profiles (DELETE /v1/groups/{id}). It is an admin
// request.
//
// When the running profile belongs to the group, Throne stops it first, and unless
// NoWait is given it responds once the group is deleted. If the wait ends first,
// DeleteGroup returns the Deletion in state StateStopping without an error, and Throne
// deletes the group once the profile has stopped.
func (a api) DeleteGroup(ctx context.Context, id int, opts ...Option) (*Deletion, error) {
	path := "/v1/groups/" + strconv.Itoa(id)
	return fetch[Deletion](ctx, a, waiting(ctx, "DELETE", path, map[string]any{}, collect(opts), deleteWait))
}

// DeleteProfiles deletes the selected profiles (DELETE /v1/profiles). It is an admin
// request. The running profile among them is stopped first, which DeleteProfiles waits
// for like DeleteGroup.
func (a api) DeleteProfiles(ctx context.Context, sel Selection, opts ...Option) (*Deletion, error) {
	return fetch[Deletion](ctx, a, waiting(ctx, "DELETE", "/v1/profiles", sel.fields(map[string]any{}), collect(opts), deleteWait))
}

// ProfileConfig returns the configuration of a profile (GET /v1/profiles/{id}/config). It
// is an admin request, since the configuration holds the profile's credentials.
func (a api) ProfileConfig(ctx context.Context, id int) (*ProfileConfig, error) {
	return fetch[ProfileConfig](ctx, a, &call{method: "GET", path: "/v1/profiles/" + strconv.Itoa(id) + "/config"})
}

// ConfigSource is the new configuration of SetProfileConfig: Outbound or Link, not both.
type ConfigSource struct {
	// Outbound is a JSON object as ProfileConfig returns it.
	Outbound json.RawMessage
	// Link is a share link of the profile's type.
	Link string
}

// SetProfileConfig replaces the configuration of a profile (PUT /v1/profiles/{id}/config)
// and returns the profile afterwards. It is an admin request.
//
// The type of the profile stays: Throne reads src as a configuration of that type, checks
// it as an import would, and changes nothing when something is wrong; that fails with
// CodeInvalidProfile, whose message has a line per problem. A profile that only Throne's
// window may edit (ProfileConfig.Editable) fails with CodeNotEditable.
func (a api) SetProfileConfig(ctx context.Context, id int, src ConfigSource) (*ProfileChange, error) {
	body := map[string]any{}
	switch {
	case len(src.Outbound) > 0 && src.Link != "":
		return nil, errors.New("throne: SetProfileConfig takes Outbound or Link, not both")
	case len(src.Outbound) > 0:
		body["outbound"] = src.Outbound
	case src.Link != "":
		body["link"] = src.Link
	default:
		return nil, errors.New("throne: SetProfileConfig needs Outbound or Link")
	}
	path := "/v1/profiles/" + strconv.Itoa(id) + "/config"
	return fetch[ProfileChange](ctx, a, &call{method: "PUT", path: path, body: body})
}

// RouteEdit changes a routing profile in AddRoute and UpdateRoute. A nil field stays as it
// is.
type RouteEdit struct {
	// Name must not be empty.
	Name *string
	// DefaultOutbound is OutboundProxy, OutboundDirect, OutboundBlock or OutboundWarpBypass.
	DefaultOutbound *int
	// Rules replace every rule, in order; a RouteKindRaw profile takes none. A RuleCustom
	// rule takes the attributes of RouteSchema plus "wifi_ssid", "wifi_bssid" and
	// "sniffers". A simple rule takes its name, Disabled and the attributes Throne's window
	// lets you edit in it, and a RuleEndpoint rule Disabled and the "outbound" attribute,
	// its profile.
	Rules []RouteRule
	// Simple replaces the simple rules that it sets; a RouteKindRaw profile takes none.
	Simple               *SimpleRulesEdit
	RawRoute             *string
	PreventModifications *bool
	// RemoteURL is an http or https URL.
	RemoteURL  *string
	AutoUpdate *bool
	// Endpoints and InnerHopEndpoints replace the lists. InnerHopEndpoints are some of
	// Endpoints.
	Endpoints         []int
	InnerHopEndpoints []int
	CustomDNS         *bool
	DNS               *RouteDNS
}

// SimpleRulesEdit changes the simple rules of a routing profile, each written as
// SimpleRules has it. A nil field stays as it is.
type SimpleRulesEdit struct {
	Proxy      *string `json:"proxy,omitempty"`
	Direct     *string `json:"direct,omitempty"`
	Block      *string `json:"block,omitempty"`
	WarpBypass *string `json:"warp_bypass,omitempty"`
}

// fields adds the fields of e that are set to a request body.
func (e RouteEdit) fields(body map[string]any) map[string]any {
	set(body, "name", e.Name)
	set(body, "default_outbound", e.DefaultOutbound)
	if e.Rules != nil {
		body["rules"] = e.Rules
	}
	if e.Simple != nil {
		body["simple"] = e.Simple
	}
	set(body, "raw_route", e.RawRoute)
	set(body, "prevent_modifications", e.PreventModifications)
	set(body, "remote_url", e.RemoteURL)
	set(body, "auto_update", e.AutoUpdate)
	if e.Endpoints != nil {
		body["endpoints"] = e.Endpoints
	}
	if e.InnerHopEndpoints != nil {
		body["inner_hop_endpoints"] = e.InnerHopEndpoints
	}
	set(body, "custom_dns", e.CustomDNS)
	if e.DNS != nil {
		body["dns"] = e.DNS
	}
	return body
}

func set[T any](body map[string]any, name string, v *T) {
	if v != nil {
		body[name] = *v
	}
}

// AddRoute adds a routing profile of kind RouteKindRules or RouteKindRaw (POST /v1/routes)
// and returns it. It is an admin request. e.Name is required, and the other fields of e
// apply as in UpdateRoute. A RouteKindRules profile without Rules starts with the rules a
// new one starts with in Throne's window.
func (a api) AddRoute(ctx context.Context, kind string, e RouteEdit) (*RouteChange, error) {
	return fetch[RouteChange](ctx, a, &call{method: "POST", path: "/v1/routes", body: e.fields(map[string]any{"kind": kind})})
}

// UpdateRoute changes a routing profile (PUT /v1/routes/{id}) and returns it. It is an
// admin request. The fields of e that are set replace those of the routing profile.
// Throne checks everything first and changes nothing when something is wrong; that fails
// with CodeInvalidRoute, whose message has a line per problem.
func (a api) UpdateRoute(ctx context.Context, id int, e RouteEdit) (*RouteChange, error) {
	return fetch[RouteChange](ctx, a, &call{method: "PUT", path: "/v1/routes/" + strconv.Itoa(id), body: e.fields(map[string]any{})})
}

// DeleteRoute deletes a routing profile (DELETE /v1/routes/{id}) and reports whether
// changes wait for the running profile to restart. It is an admin request. Throne keeps
// at least one routing profile: deleting the last fails with CodeLastRoute.
func (a api) DeleteRoute(ctx context.Context, id int) (bool, error) {
	r, err := fetch[struct {
		RestartNeeded bool `json:"restart_needed"`
	}](ctx, a, &call{method: "DELETE", path: "/v1/routes/" + strconv.Itoa(id)})
	if err != nil {
		return false, err
	}
	return r.RestartNeeded, nil
}

// Settings returns Throne's settings that the API manages (GET /v1/settings), by the
// names spec/api.md lists. Values decode as encoding/json does: bool, float64 or string.
// Settings this platform lacks are absent.
func (a api) Settings(ctx context.Context) (map[string]any, error) {
	r, err := fetch[struct {
		Settings map[string]any `json:"settings"`
	}](ctx, a, &call{method: "GET", path: "/v1/settings"})
	if err != nil {
		return nil, err
	}
	return r.Settings, nil
}

// SetSettings changes settings (PUT /v1/settings) and returns all of them afterwards. It
// is an admin request. Throne checks every value first and changes nothing when one is
// wrong. A setting given its current value is left alone, so values that Settings
// returned can be sent back with some of them changed.
func (a api) SetSettings(ctx context.Context, values map[string]any) (*SettingsChange, error) {
	if values == nil {
		values = map[string]any{}
	}
	return fetch[SettingsChange](ctx, a, &call{method: "PUT", path: "/v1/settings", body: map[string]any{"settings": values}})
}

// WarpSettings are what SetWarp changes. A nil or "" field stays as it is.
type WarpSettings struct {
	Enabled *bool
	// Mode is WarpWireGuard, WarpAmnezia or WarpMASQUE.
	Mode string
}

// SetWarp turns WARP on or off and picks its mode (PUT /v1/warp), and returns WARP
// afterwards. It is an admin request. Turning WARP on in a mode without a registered
// device fails with CodeWarpNotConfigured; RegisterWarp registers one.
func (a api) SetWarp(ctx context.Context, w WarpSettings) (*WarpChange, error) {
	body := map[string]any{}
	set(body, "enabled", w.Enabled)
	if w.Mode != "" {
		body["mode"] = w.Mode
	}
	return fetch[WarpChange](ctx, a, &call{method: "PUT", path: "/v1/warp", body: body})
}

// RegisterWarp registers a new WARP device with Cloudflare for mode, "" for the current
// one (POST /v1/warp/register), and returns WARP afterwards, with mode as its mode;
// Enabled stays as it is. It is an admin request.
//
// Registering needs Cloudflare's WARP terms (Warp.TermsURL) accepted once: acceptTerms
// accepts them, and until they are, RegisterWarp fails with CodeTermsRequired. Throne
// responds within 90 seconds. A failed registration stores nothing and fails with
// CodeWarpFailed.
func (a api) RegisterWarp(ctx context.Context, mode string, acceptTerms bool) (*WarpChange, error) {
	body := map[string]any{}
	if mode != "" {
		body["mode"] = mode
	}
	if acceptTerms {
		body["accept_terms"] = true
	}
	return fetch[WarpChange](ctx, a, &call{method: "POST", path: "/v1/warp/register", body: body, wait: warpWait})
}

// Logs returns lines of Throne's log, the log that Throne's window shows (GET
// /v1/logs). It is an admin request, since the log names the sites that connections
// went to. With q.Instance and q.Wait, Throne holds the request while no line follows
// q.After, so a loop of Logs calls, each with the Instance and Cursor of the last, follows
// the log.
func (a api) Logs(ctx context.Context, q LogQuery) (*Logs, error) {
	query := map[string]string{}
	if q.Limit > 0 {
		query["limit"] = strconv.Itoa(min(q.Limit, maxLogLines))
	}
	r := &call{method: "GET", path: "/v1/logs", query: query}
	if q.Instance != "" {
		query["instance"] = q.Instance
		query["after"] = strconv.FormatInt(q.After, 10)
		if t := waitSeconds(q.Wait); t > 0 {
			query["timeout"] = strconv.Itoa(t)
			r.wait = time.Duration(t) * time.Second
		}
	}
	return fetch[Logs](ctx, a, r)
}

// Connections returns the connections of the running profile (GET /v1/connections). It is
// an admin request, since connections name the sites they go to.
//
// Each call makes Throne keep its list fresh, once a second, for the next 10 seconds. So
// the first call after a pause can return an old or empty list, as UpdatedAt tells, and a
// call a second later gets a fresh one.
func (a api) Connections(ctx context.Context) (*Connections, error) {
	return fetch[Connections](ctx, a, &call{method: "GET", path: "/v1/connections"})
}

// CloseConnections closes the connections with these IDs (DELETE /v1/connections) and
// returns how many the core closed. Without IDs it closes none and sends nothing. It is an
// admin request. It fails with CodeNotRunning when no profile runs.
func (a api) CloseConnections(ctx context.Context, ids ...string) (int, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	return a.closeConnections(ctx, map[string]any{"ids": ids})
}

// CloseAllConnections closes every open connection of Throne's latest list
// (DELETE /v1/connections), as CloseConnections does.
func (a api) CloseAllConnections(ctx context.Context) (int, error) {
	return a.closeConnections(ctx, map[string]any{"all": true})
}

func (a api) closeConnections(ctx context.Context, body map[string]any) (int, error) {
	r, err := fetch[struct {
		Closed int `json:"closed"`
	}](ctx, a, &call{method: "DELETE", path: "/v1/connections", body: body})
	if err != nil {
		return 0, err
	}
	return r.Closed, nil
}

// UpdateRuleSets makes the core download the remote rule-sets of the running profile
// again (POST /v1/rule-sets/update). It fails with CodeNotRunning when no profile runs.
//
// Unless NoWait is given, Throne responds once the update has finished. If the wait ends
// first, UpdateRuleSets returns the RuleSetUpdate in state StateUpdating without an
// error, and the update carries on in Throne.
func (a api) UpdateRuleSets(ctx context.Context, opts ...Option) (*RuleSetUpdate, error) {
	return fetch[RuleSetUpdate](ctx, a, waiting(ctx, "POST", "/v1/rule-sets/update", map[string]any{}, collect(opts), ruleSetWait))
}

// GeoAssets lists the geoip and geosite files (GET /v1/geo-assets).
func (a api) GeoAssets(ctx context.Context) ([]GeoAsset, error) {
	r, err := fetch[struct {
		Files []GeoAsset `json:"files"`
	}](ctx, a, &call{method: "GET", path: "/v1/geo-assets"})
	if err != nil {
		return nil, err
	}
	return r.Files, nil
}

// UpdateGeoAssets downloads geo files again (POST /v1/geo-assets/update), one after
// another: files names them, "geoip" and "geosite", and nil means both. With OnlyMissing
// it skips the files that are present.
//
// Unless NoWait is given, Throne responds once the downloads have finished. If the wait
// ends first, UpdateGeoAssets returns the GeoUpdate in state StateDownloading without an
// error, and the downloads carry on in Throne. It fails with CodeBusy when one of the
// files is already downloading.
func (a api) UpdateGeoAssets(ctx context.Context, files []string, opts ...Option) (*GeoUpdate, error) {
	o := collect(opts)
	body := map[string]any{}
	if len(files) > 0 {
		body["files"] = files
	}
	if o.onlyMissing {
		body["only_missing"] = true
	}
	return fetch[GeoUpdate](ctx, a, waiting(ctx, "POST", "/v1/geo-assets/update", body, o, geoWait))
}

// Restart restarts Throne (POST /v1/restart) and reports whether Throne starts the
// running profile again after the restart. It is an admin request. Throne responds
// before it exits, and asks nothing in its window.
func (a api) Restart(ctx context.Context) (bool, error) {
	r, err := fetch[struct {
		Resume bool `json:"resume"`
	}](ctx, a, &call{method: "POST", path: "/v1/restart"})
	if err != nil {
		return false, err
	}
	return r.Resume, nil
}

// Shutdown makes Throne exit (POST /v1/shutdown), as Exit in its window does. It is an
// admin request. Throne responds before it exits.
func (a api) Shutdown(ctx context.Context) error {
	return a.do(ctx, &call{method: "POST", path: "/v1/shutdown"}, nil)
}

// Backup writes a backup archive of parts to w and describes it. Nil parts means every
// part; the archive leaves out PartIcons when Throne has no icon files, and BackupInfo
// lists the parts it holds. It is an admin request. The archive holds credentials in
// plain text.
//
// Throne builds the archive (POST /v1/backups). Backup downloads it in chunks
// (GET /v1/backups/{id}), checks its size and SHA-256, and deletes it in Throne
// (DELETE /v1/backups/{id}), after an error too. Unless NoWait is given, Throne responds
// once the archive is built; if the wait ends first, or with NoWait, Backup polls until
// it is. After an error w may hold part of the archive.
func (a api) Backup(ctx context.Context, w io.Writer, parts []string, opts ...Option) (*BackupInfo, error) {
	body := map[string]any{}
	if len(parts) > 0 {
		body["parts"] = parts
	}
	// thronectl prints the BackupInfo, not the body of the last request.
	inner := rawbody.With(ctx, new(json.RawMessage))
	var created struct {
		BackupInfo
		State string `json:"state"`
	}
	if err := a.do(inner, waiting(inner, "POST", "/v1/backups", body, collect(opts), backupWait), &created); err != nil {
		return nil, err
	}
	info := created.BackupInfo
	path := "/v1/backups/" + strconv.Itoa(info.ID)
	err := a.download(inner, w, path, &info, created.State != "")
	a.discard(inner, path)
	if err != nil {
		return nil, err
	}
	if raw, err := json.Marshal(info); err == nil {
		rawbody.Store(ctx, raw)
	}
	return &info, nil
}

// download writes the archive at path to w and checks it against info. building says
// that Throne had not finished the archive: download then polls while Throne answers
// CodeBusy and takes the size, SHA-256 and parts from the first chunk.
func (a api) download(ctx context.Context, w io.Writer, path string, info *BackupInfo, building bool) error {
	h := sha256.New()
	var offset int64
	for {
		var chunk struct {
			Size   int64    `json:"size"`
			SHA256 string   `json:"sha256"`
			Parts  []string `json:"parts"`
			Offset int64    `json:"offset"`
			Data   []byte   `json:"data"`
			EOF    bool     `json:"eof"`
		}
		query := map[string]string{"offset": strconv.FormatInt(offset, 10), "length": strconv.Itoa(backupChunk)}
		err := a.do(ctx, &call{method: "GET", path: path, query: query}, &chunk)
		if building && ErrorCode(err) == CodeBusy {
			if err := pause(ctx); err != nil {
				return err
			}
			continue
		}
		if err != nil {
			return err
		}
		if building {
			info.Size, info.SHA256, info.Parts, building = chunk.Size, chunk.SHA256, chunk.Parts, false
		}
		switch end := offset + int64(len(chunk.Data)); {
		case chunk.Size != info.Size || !strings.EqualFold(chunk.SHA256, info.SHA256):
			return errors.New("throne: the backup changed during the download")
		case chunk.Offset != offset:
			return fmt.Errorf("throne: backup chunk at offset %d, want %d", chunk.Offset, offset)
		case end > info.Size:
			return fmt.Errorf("throne: backup chunk ends at %d, past the size of %d bytes", end, info.Size)
		case len(chunk.Data) == 0 && !chunk.EOF:
			return fmt.Errorf("throne: empty backup chunk at offset %d", offset)
		}
		if _, err := w.Write(chunk.Data); err != nil {
			return fmt.Errorf("throne: write backup: %w", err)
		}
		h.Write(chunk.Data)
		offset += int64(len(chunk.Data))
		if chunk.EOF {
			break
		}
	}
	if offset != info.Size {
		return fmt.Errorf("throne: the backup ended after %d of %d bytes", offset, info.Size)
	}
	if !strings.EqualFold(hex.EncodeToString(h.Sum(nil)), info.SHA256) {
		return errors.New("throne: the backup does not match its SHA-256")
	}
	return nil
}

// UploadRestore uploads a backup archive for a restore, from r's current offset to its
// end, and returns the upload with the archive as Throne read it. It is an admin request.
//
// It opens the upload with the archive's size and SHA-256 (POST /v1/restores) and sends
// the archive in chunks (PUT /v1/restores/{id}). Throne checks the archive when the last
// chunk arrives, and UploadRestore waits for that: if Throne's wait ends first, it polls
// the upload (GET /v1/restores/{id}) until Throne has checked the archive or ctx ends. An
// archive that Throne refuses fails with CodeInvalidArchive. After an error UploadRestore
// discards the upload in Throne, best effort.
//
// Restore the upload with ApplyRestore or discard it with CancelRestore. Throne discards
// it by itself 10 minutes after the last request for it.
func (a api) UploadRestore(ctx context.Context, r io.ReadSeeker) (*RestoreUpload, error) {
	start, err := r.Seek(0, io.SeekCurrent)
	if err != nil {
		return nil, fmt.Errorf("throne: read archive: %w", err)
	}
	h := sha256.New()
	size, err := io.Copy(h, r)
	if err != nil {
		return nil, fmt.Errorf("throne: read archive: %w", err)
	}
	if size == 0 {
		return nil, errors.New("throne: the archive is empty")
	}
	if _, err := r.Seek(start, io.SeekStart); err != nil {
		return nil, fmt.Errorf("throne: read archive: %w", err)
	}
	var open struct {
		ID         int `json:"id"`
		ChunkLimit int `json:"chunk_limit"`
	}
	body := map[string]any{"size": size, "sha256": hex.EncodeToString(h.Sum(nil))}
	if err := a.do(ctx, &call{method: "POST", path: "/v1/restores", body: body}, &open); err != nil {
		return nil, err
	}
	path := "/v1/restores/" + strconv.Itoa(open.ID)
	n := maxUploadChunk
	if open.ChunkLimit > 0 {
		n = min(n, open.ChunkLimit)
	}
	up, err := a.upload(ctx, r, path, size, n)
	if err == nil && up.Archive == nil {
		up, err = a.inspected(ctx, open.ID)
	}
	if err != nil {
		a.discard(ctx, path)
		return nil, err
	}
	if up.State == "" {
		up.State = UploadComplete
	}
	return up, nil
}

// inspected polls an upload until Throne has checked its archive.
func (a api) inspected(ctx context.Context, id int) (*RestoreUpload, error) {
	for {
		up, err := a.RestoreStatus(ctx, id)
		if err != nil {
			return nil, err
		}
		switch {
		case up.State == UploadComplete && up.Archive != nil:
			return up, nil
		case up.State == UploadFailed:
			return nil, &APIError{Status: 422, Code: CodeInvalidArchive, Message: up.Error}
		case up.State != UploadInspecting:
			return nil, fmt.Errorf("throne: upload %d is %q after its last chunk", id, up.State)
		}
		if err := pause(ctx); err != nil {
			return nil, err
		}
	}
}

// upload sends size bytes of r to the upload at path in chunks of up to n bytes; the
// last one carries the waiting fields.
func (a api) upload(ctx context.Context, r io.Reader, path string, size int64, n int) (*RestoreUpload, error) {
	buf := make([]byte, min(int64(n), size))
	var up *RestoreUpload
	for offset := int64(0); offset < size; {
		chunk := buf[:min(int64(len(buf)), size-offset)]
		if _, err := io.ReadFull(r, chunk); err != nil {
			return nil, fmt.Errorf("throne: read archive: %w", err)
		}
		body := map[string]any{"offset": offset, "data": chunk}
		c := &call{method: "PUT", path: path, body: body}
		if offset+int64(len(chunk)) == size {
			c = waiting(ctx, "PUT", path, body, options{}, uploadWait)
		}
		var err error
		if up, err = fetch[RestoreUpload](ctx, a, c); err != nil {
			return nil, err
		}
		offset += int64(len(chunk))
	}
	return up, nil
}

// ApplyRestore replaces parts of Throne's data with those of an uploaded archive
// (POST /v1/restores/{id}/apply), and Throne restarts. Nil parts means the archive's
// parts except PartSettings and PartIcons. It is an admin request.
//
// Throne stops the running profile first, and unless NoWait is given it responds once the
// parts are replaced. If the wait ends first, ApplyRestore returns the RestoreResult in
// state StateApplying without an error, and the restore carries on in Throne. Until
// Throne is back from the restart, every request fails with CodeRestarting. Headless
// Throne (Capabilities.Headless) answers so only until it exits or starts again in the
// same process; then connections fail until it listens again, and requests fail with
// CodeStarting until it has started.
//
// Parts that hold profiles that would run programs or read files on Throne's device, or
// subscription or routing profile URLs that are not http or https, fail with
// CodeInvalidArchive, and nothing is replaced; restore such an archive in Throne's window.
func (a api) ApplyRestore(ctx context.Context, id int, parts []string, opts ...Option) (*RestoreResult, error) {
	body := map[string]any{}
	if len(parts) > 0 {
		body["parts"] = parts
	}
	path := "/v1/restores/" + strconv.Itoa(id) + "/apply"
	return fetch[RestoreResult](ctx, a, waiting(ctx, "POST", path, body, collect(opts), applyWait))
}

// RestoreStatus returns an upload as it stands (GET /v1/restores/{id}): the bytes Throne
// has, and once it has checked the archive, the Archive or why it refused it. It is an
// admin request.
func (a api) RestoreStatus(ctx context.Context, id int) (*RestoreUpload, error) {
	return fetch[RestoreUpload](ctx, a, &call{method: "GET", path: "/v1/restores/" + strconv.Itoa(id)})
}

// CancelRestore discards an uploaded archive (DELETE /v1/restores/{id}). It is an admin
// request.
func (a api) CancelRestore(ctx context.Context, id int) error {
	return a.do(ctx, &call{method: "DELETE", path: "/v1/restores/" + strconv.Itoa(id)}, nil)
}

// discard deletes a backup or an upload in Throne, best effort, also after ctx ended.
func (a api) discard(ctx context.Context, path string) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), discardWait)
	defer cancel()
	a.do(ctx, &call{method: "DELETE", path: path}, nil)
}
