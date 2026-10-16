package throne

// Profile is a Throne profile. It never carries credentials or outbound configuration.
type Profile struct {
	ID    int    `json:"id"`
	Group int    `json:"group"`
	Name  string `json:"name"`
	// Type is Throne's profile type, such as "vless", "shadowsocks", "chain" or
	// "autoselector".
	Type string `json:"type"`
	// Address is the address Throne shows; for an auto selector, the name of the group
	// it tracks.
	Address string `json:"address"`
	// LatencyMS is the last URL test: above 0 the latency in milliseconds, 0 not tested,
	// -1 failed, -2 the tunnel came up but the test request failed.
	LatencyMS int `json:"latency_ms"`
	// LatencyAt is when LatencyMS was measured, in Unix seconds.
	LatencyAt int64 `json:"latency_at"`
	// DownloadBPS and UploadBPS are the last speed test in bits per second: 0 not
	// tested, -1 failed.
	DownloadBPS int64 `json:"download_bps"`
	UploadBPS   int64 `json:"upload_bps"`
	// Country is the ISO 3166-1 alpha-2 code from the last test that reported one.
	Country string `json:"country"`
	// IPOut is the exit IP address from the last IP test.
	IPOut string `json:"ip_out"`
	// TrafficUp and TrafficDown are the bytes sent and received through the profile.
	TrafficUp   int64 `json:"traffic_up"`
	TrafficDown int64 `json:"traffic_down"`
	// Running says whether Throne runs this profile.
	Running bool `json:"running"`
}

// Group is a group of profiles.
type Group struct {
	ID           int    `json:"id"`
	Name         string `json:"name"`
	Subscription bool   `json:"subscription"`
	Archived     bool   `json:"archived"`
	ProfileCount int    `json:"profile_count"`
	// UpdatedAt is the last subscription update in Unix seconds, 0 without a subscription.
	UpdatedAt int64 `json:"updated_at"`
}

// Route is a routing profile.
type Route struct {
	ID     int    `json:"id"`
	Name   string `json:"name"`
	Active bool   `json:"active"`
}

// Values of Status.State.
const (
	StateIdle       = "idle"
	StateConnecting = "connecting"
	StateRunning    = "running"
	StateStopping   = "stopping"
)

// Status is Throne's state.
type Status struct {
	// State is StateIdle, StateConnecting, StateRunning or StateStopping.
	State string `json:"state"`
	// Profile is the running profile, or nil.
	Profile *Profile `json:"profile"`
	// LastProfileID is the profile ConnectLast would start, or -1.
	LastProfileID int `json:"last_profile_id"`
	// Tun and SystemProxy say whether the modes are on; they apply while a profile runs.
	Tun         bool `json:"tun"`
	SystemProxy bool `json:"system_proxy"`
	// Route is the active routing profile.
	Route Route `json:"route"`
	// KillSwitch is "off", "arming", "armed" or "failed".
	KillSwitch string `json:"kill_switch"`
	// Core says whether Throne's core process is up.
	Core bool `json:"core"`
	// Inbound is Throne's mixed (SOCKS and HTTP) proxy inbound.
	Inbound Inbound `json:"inbound"`
	// Version is Throne's version.
	Version string `json:"version"`
	// Capabilities say what this Throne can do.
	Capabilities Capabilities `json:"capabilities"`
	// RestartNeeded says that changes, such as settings, wait for the running profile to
	// restart. Connect with Restart applies them.
	RestartNeeded bool `json:"restart_needed"`
	// Degraded is "", or why Throne cannot work as set up: DegradedCoreUnavailable or
	// DegradedKillSwitchFailed.
	Degraded string `json:"degraded"`
}

// Values of Status.Degraded.
const (
	DegradedCoreUnavailable  = "core_unavailable"   // the core process is not up
	DegradedKillSwitchFailed = "kill_switch_failed" // the kill switch is on and failed
)

// Capabilities say what a Throne can do.
type Capabilities struct {
	// Tun says whether Throne has the privileges Tun needs, as far as it knows without
	// asking, or is set not to ask for them. It is nil while Throne does not know yet: on
	// Linux and macOS, before the core connects or while its privilege check runs.
	Tun *bool `json:"tun"`
	// KillSwitch says whether the kill switch works on this system.
	KillSwitch bool `json:"kill_switch"`
	// Headless says whether Throne runs without a window.
	Headless bool `json:"headless"`
}

// Inbound is Throne's mixed proxy inbound.
type Inbound struct {
	Enabled bool   `json:"enabled"`
	Address string `json:"address"`
	Port    int    `json:"port"`
}

// TestKind is the kind of a test.
type TestKind string

// Test kinds.
const (
	TestLatency TestKind = "latency" // Throne's URL test
	TestIP      TestKind = "ip"      // exit IP test
	TestSpeed   TestKind = "speed"   // speed test, in the mode set in Throne
)

// Values of Job.State.
const (
	JobRunning   = "running"
	JobDone      = "done"
	JobCancelled = "cancelled"
)

// Job is a test that the API started.
type Job struct {
	ID   int      `json:"id"`
	Kind TestKind `json:"kind"`
	// State is JobRunning, JobDone or JobCancelled.
	State string `json:"state"`
	// ProfileIDs are the profiles being tested, in test order.
	ProfileIDs []int `json:"profile_ids"`
	StartedAt  int64 `json:"started_at"`
	FinishedAt int64 `json:"finished_at"`
	// Results are the tested profiles as they are after the test. They are present
	// once State is not JobRunning, except in the list from Jobs.
	Results []Profile `json:"results,omitempty"`
}

// LiveStats are the current traffic rates, in bytes per second over the last second.
type LiveStats struct {
	Proxy  Rates `json:"proxy"`
	Direct Rates `json:"direct"`
}

// Rates are upload and download rates in bytes per second.
type Rates struct {
	Up   int64 `json:"up"`
	Down int64 `json:"down"`
}

// Groupings of TrafficQuery.By.
const (
	TrafficByProfile = "profile"
	TrafficByApp     = "app"
)

// TrafficStats are traffic totals over a period, largest total first.
type TrafficStats struct {
	By    string        `json:"by"`
	From  int64         `json:"from"`
	To    int64         `json:"to"`
	Items []TrafficItem `json:"items"`
}

// TrafficItem is the traffic of one profile or app.
type TrafficItem struct {
	// ID is the profile ID, -101 for direct traffic, and nil when grouping by app.
	ID *int `json:"id,omitempty"`
	// Name is the profile name, or the process name when grouping by app.
	Name string `json:"name"`
	Up   int64  `json:"up"`
	Down int64  `json:"down"`
}

// Values of State in the response of a management request whose wait ended first; the
// work carries on in Throne. DeleteGroup and DeleteProfiles use StateStopping.
const (
	StateQueued      = "queued"      // Import
	StateUpdating    = "updating"    // UpdateRuleSets
	StateDownloading = "downloading" // UpdateGeoAssets
	StateApplying    = "applying"    // ApplyRestore
)

// ImportResult is the outcome of Import.
type ImportResult struct {
	// Group is the group the profiles went into.
	Group int `json:"group"`
	// State is StateQueued when the wait ended first: the import carries on in Throne and
	// the other fields are empty. Otherwise it is "".
	State string `json:"state,omitempty"`
	// Imported is how many profiles were added, and ProfileIDs are their IDs.
	Imported   int   `json:"imported"`
	ProfileIDs []int `json:"profile_ids"`
	// Failed counts the entries that could not be imported, and Warned those imported
	// with a warning.
	Failed int `json:"failed"`
	Warned int `json:"warned"`
	// Issues are the problems of the entries, alike ones together.
	Issues []ImportIssue `json:"issues"`
	// Errors are why the whole document failed, such as a configuration that does not
	// parse.
	Errors []string `json:"errors"`
}

// Values of ImportIssue.Severity.
const (
	SeverityError   = "error"   // the entries were not imported
	SeverityWarning = "warning" // the entries were imported
)

// ImportIssue is a problem that entries of an import had.
type ImportIssue struct {
	// Severity is SeverityError or SeverityWarning.
	Severity string `json:"severity"`
	// Field is the field of the entry that the problem concerns, such as "port".
	Field   string `json:"field"`
	Message string `json:"message"`
	// Count is how many entries had the problem, and Examples are a few of them, without
	// credentials.
	Count    int      `json:"count"`
	Examples []string `json:"examples"`
}

// UpdateResult is the outcome of a subscription update.
type UpdateResult struct {
	// OK says whether the update succeeded; Error says why it did not.
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
	// Added, Updated and Removed count the profiles the update added, changed and
	// removed. Kept counts those the subscription no longer lists that Throne kept
	// because they are in use, such as the running profile.
	Added   int `json:"added"`
	Updated int `json:"updated"`
	Removed int `json:"removed"`
	Kept    int `json:"kept"`
}

// GroupResult is a group and the outcome of its subscription update.
type GroupResult struct {
	Group Group `json:"group"`
	// Update is nil when no update ran, or when the wait ended first: the update then
	// carries on in Throne.
	Update *UpdateResult `json:"update"`
}

// UpdateGroupsResult is the outcome of UpdateGroups.
type UpdateGroupsResult struct {
	// Results are the updates of the groups, in tab order. They are nil when the wait
	// ended first.
	Results []GroupUpdate `json:"results"`
	// Queued are the groups being updated when the wait ended first, else nil.
	Queued []int `json:"queued,omitempty"`
}

// GroupUpdate is the outcome of the update of one group.
type GroupUpdate struct {
	ID int `json:"id"`
	UpdateResult
}

// Deletion is the outcome of DeleteGroup or DeleteProfiles.
type Deletion struct {
	// Deleted is the group's ID for DeleteGroup, the number of profiles for
	// DeleteProfiles.
	Deleted int `json:"deleted"`
	// State is StateStopping when the wait ended while Throne stopped the running
	// profile: the delete follows the stop. Otherwise it is "".
	State string `json:"state,omitempty"`
}

// SettingsChange is the outcome of SetSettings.
type SettingsChange struct {
	// Settings are all the settings after the change, as Settings returns them.
	Settings map[string]any `json:"settings"`
	// Changed names the settings whose values changed, sorted.
	Changed []string `json:"changed"`
	// RestartNeeded says that a change waits for the running profile to restart.
	RestartNeeded bool `json:"restart_needed"`
}

// RuleSetUpdate is the outcome of UpdateRuleSets.
type RuleSetUpdate struct {
	// State is StateUpdating when the wait ended first: the update carries on in Throne
	// and the other fields are empty. Otherwise it is "".
	State string `json:"state,omitempty"`
	// Updated counts the rule-sets the core downloaded again.
	Updated  int              `json:"updated"`
	Failures []RuleSetFailure `json:"failures"`
}

// RuleSetFailure is a rule-set that failed to update.
type RuleSetFailure struct {
	Tag   string `json:"tag"`
	Error string `json:"error"`
}

// GeoAsset is a geoip or geosite file.
type GeoAsset struct {
	// Name is the file name, such as "geoip.dat".
	Name string `json:"name"`
	// URL is where Throne downloads the file from.
	URL string `json:"url"`
	// Present says whether the file exists. Size and UpdatedAt, when it last changed in
	// Unix seconds, are 0 when it does not.
	Present   bool  `json:"present"`
	Size      int64 `json:"size"`
	UpdatedAt int64 `json:"updated_at"`
}

// GeoUpdate is the outcome of UpdateGeoAssets.
type GeoUpdate struct {
	// State is StateDownloading when the wait ended first: the downloads carry on in
	// Throne and Files is nil. Otherwise it is "".
	State string          `json:"state,omitempty"`
	Files []GeoFileResult `json:"files"`
}

// GeoFileResult is the outcome of the download of one geo file.
type GeoFileResult struct {
	// Name is the file name, such as "geoip.dat".
	Name string `json:"name"`
	// OK says whether the file is in place; Error says why not. Skipped says that it was
	// present already and OnlyMissing was given.
	OK      bool   `json:"ok"`
	Skipped bool   `json:"skipped,omitempty"`
	Error   string `json:"error,omitempty"`
}

// Parts of a backup archive.
const (
	PartProfiles = "profiles" // groups and profiles
	PartRoutes   = "routes"   // routing profiles and their rules
	PartSettings = "settings" // settings
	PartOTP      = "otp"      // OTP accounts, secrets included
	PartIPLists  = "ip_lists" // IP lists and IP scans
	PartIcons    = "icons"    // icon files
)

// BackupInfo describes a backup archive that Backup downloaded.
type BackupInfo struct {
	ID int `json:"id"`
	// Size is the archive's size in bytes, and SHA256 its SHA-256 in hex.
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
	// Parts are the parts the archive holds.
	Parts []string `json:"parts"`
}

// Values of RestoreUpload.State.
const (
	UploadUploading  = "uploading"  // Throne waits for more chunks
	UploadInspecting = "inspecting" // Throne checks the archive
	UploadComplete   = "complete"   // the archive is checked; Archive describes it
	UploadFailed     = "failed"     // Throne refused the archive; Error says why
)

// RestoreUpload is a backup archive uploaded for a restore.
type RestoreUpload struct {
	ID int `json:"id"`
	// Size is the archive's size in bytes, and Received how many of them Throne has.
	Size     int64 `json:"size"`
	Received int64 `json:"received"`
	// State is UploadUploading, UploadInspecting, UploadComplete or UploadFailed.
	State string `json:"state"`
	// Archive is the archive as Throne read it, once State is UploadComplete.
	Archive *Archive `json:"archive"`
	// Error is why Throne refused the archive, when State is UploadFailed.
	Error string `json:"error,omitempty"`
}

// Archive describes a backup archive.
type Archive struct {
	// CreatedAt is when the archive was made, as Throne wrote it: local time without a
	// time zone, for people to read.
	CreatedAt string `json:"created_at"`
	// Platform is the system that made it: "winnt", "linux" or "darwin".
	Platform string `json:"platform"`
	// Parts are the parts the archive holds.
	Parts []string `json:"parts"`
}

// RestoreResult is the outcome of ApplyRestore.
type RestoreResult struct {
	// State is StateApplying when the wait ended first: the restore carries on in Throne
	// and the other fields are empty. Otherwise it is "".
	State string `json:"state,omitempty"`
	// Restored are the parts that were replaced.
	Restored []string `json:"restored"`
	// SkippedRules counts the routing rules of the archive that this Throne could not
	// read.
	SkippedRules int `json:"skipped_rules"`
	// Restarting says that Throne restarts now. It is false when the wait ended first:
	// the restore then carries on in Throne.
	Restarting bool `json:"restarting"`
}
