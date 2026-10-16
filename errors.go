package throne

import (
	"errors"
	"fmt"
)

// Error codes: the Code of an APIError.
const (
	CodeBadRequest             = "bad_request"
	CodeNoProfiles             = "no_profiles"
	CodeForbidden              = "forbidden"
	CodeNotFound               = "not_found"
	CodeMethodNotAllowed       = "method_not_allowed"
	CodeBusy                   = "busy"
	CodeSuperseded             = "superseded"
	CodeGroupArchived          = "group_archived"
	CodeKillSwitchInactive     = "kill_switch_inactive"
	CodeKillSwitchBlocked      = "kill_switch_blocked"
	CodeMixedInboundDisabled   = "mixed_inbound_disabled"
	CodeNoWorkingProfile       = "no_working_profile"
	CodeStatsDisabled          = "stats_disabled"
	CodeEditing                = "editing"
	CodeLastGroup              = "last_group"
	CodeSubscriptionGroup      = "subscription_group"
	CodeNotSubscription        = "not_subscription"
	CodeNotRunning             = "not_running"
	CodeIncomplete             = "incomplete"
	CodeBadOffset              = "bad_offset"
	CodeNeedsPrivileges        = "needs_privileges"
	CodeTooLarge               = "too_large"
	CodeBuildFailed            = "build_failed"
	CodeGeoAssetsMissing       = "geo_assets_missing"
	CodeStrictRouteUnavailable = "strict_route_unavailable"
	CodeTunFailed              = "tun_failed"
	CodeStartFailed            = "start_failed"
	CodeNothingImported        = "nothing_imported"
	CodeChecksumMismatch       = "checksum_mismatch"
	CodeInvalidArchive         = "invalid_archive"
	CodeInternal               = "internal"
	CodeFetchFailed            = "fetch_failed"
	CodeCoreUnavailable        = "core_unavailable"
	CodeExiting                = "exiting"
	CodeRestarting             = "restarting"
)

// ErrRejected means that Throne closed the connection before it sent a complete
// response. Throne closes the connection without a word when a request fails
// authentication, so the key is probably wrong. It also refuses addresses outside its
// allowed list, connections over its limits, an address for a minute after repeated
// failures, and clients of an unsupported protocol version.
var ErrRejected = errors.New("throne: connection closed before a response: the key is probably wrong, or Throne refused this connection")

// APIError is an error response from Throne, one with a status of 400 or more.
type APIError struct {
	// Status follows HTTP status semantics, such as 409.
	Status int
	// Code is stable and meant for programs, such as CodeBusy.
	Code string
	// Message is meant for people; it may change or be translated.
	Message string
}

func (e *APIError) Error() string {
	switch {
	case e.Code == "":
		return fmt.Sprintf("throne: status %d", e.Status)
	case e.Message == "":
		return "throne: " + e.Code
	}
	return fmt.Sprintf("throne: %s: %s", e.Code, e.Message)
}

// ErrorCode returns the Code of the APIError in err's chain, or "" if there is none.
func ErrorCode(err error) string {
	var e *APIError
	if errors.As(err, &e) {
		return e.Code
	}
	return ""
}
