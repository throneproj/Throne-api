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
