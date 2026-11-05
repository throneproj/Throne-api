package fakethrone

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
	"time"

	throne "github.com/throneproj/Throne-api"
)

// routeData is what a routing profile holds besides its Route.
type routeData struct {
	kind                 string
	defaultOutbound      int
	rules                []throne.RouteRule // attribute maps are never changed once made
	raw                  string
	prevent              bool
	remoteURL            string
	autoUpdate           bool
	remoteUpdatedAt      int64
	endpoints, innerHops []int
	customDNS            bool
	dns                  *throne.RouteDNS
}

// ruleAttributes are those of Throne's rule editor on Linux, which the fake runs as.
var ruleAttributes = []string{"ip_version", "network", "protocol", "inbound", "domain", "domain_suffix", "domain_keyword",
	"domain_regex", "source_ip_cidr", "source_ip_is_private", "ip_cidr", "ip_is_private", "source_port", "source_port_range",
	"port", "port_range", "process_name", "process_path", "process_path_regex", "package_name", "wifi_ssid", "wifi_bssid",
	"rule_set", "invert", "action", "outbound", "override_address", "override_port", "tls_fragment",
	"tls_fragment_fallback_delay", "tls_record_fragment", "tls_spoof", "tls_spoof_method", "method", "no_drop",
	"override_destination", "strategy"}

var selectValues = map[string][]string{
	"ip_version":       {"", "4", "6"},
	"network":          {"", "tcp", "udp", "icmp"},
	"protocol":         {"", "http", "tls", "quic", "stun", "dns", "bittorrent", "dtls", "ssh", "rdp", "ntp"},
	"action":           {"route", "bypass", "reject", "hijack-dns", "route-options", "sniff", "resolve"},
	"method":           {"", "default", "drop", "reply"},
	"strategy":         {"", "ipv4_only", "ipv6_only", "prefer_ipv4", "prefer_ipv6"},
	"tls_spoof_method": {"", "wrong-sequence", "wrong-checksum", "wrong-ack", "wrong-md5", "wrong-timestamp"},
}

var boolAttributes = []string{"invert", "source_ip_is_private", "ip_is_private", "no_drop", "tls_fragment",
	"tls_record_fragment", "override_destination"}

var listAttributes = []string{"inbound", "domain", "domain_suffix", "domain_keyword", "domain_regex", "source_ip_cidr",
	"ip_cidr", "source_port", "source_port_range", "port", "port_range", "process_name", "process_path",
	"process_path_regex", "package_name", "wifi_ssid", "wifi_bssid", "rule_set", "sniffers"}

var routeLike = []string{"route", "route-options", "bypass"}

var actionScopes = map[string][]string{
	"outbound": {"route", "bypass"}, "override_address": routeLike, "override_port": routeLike, "tls_fragment": routeLike,
	"tls_fragment_fallback_delay": routeLike, "tls_record_fragment": routeLike, "tls_spoof": routeLike,
	"tls_spoof_method": routeLike, "method": {"reject"}, "no_drop": {"reject"}, "override_destination": {"sniff"},
	"strategy": {"resolve"},
}

var ruleTypes = []string{throne.RuleCustom, "simple_address_proxy", "simple_address_bypass", "simple_address_block",
	"simple_process_name_proxy", "simple_process_name_bypass", "simple_process_name_block", "simple_process_path_proxy",
	"simple_process_path_bypass", "simple_process_path_block", "simple_address_warp_bypass",
	"simple_process_name_warp_bypass", "simple_process_path_warp_bypass", throne.RuleEndpoint}

// simpleKind is the simple rules of one action, under key in the simple object.
type simpleKind struct {
	key, suffix, label, title, action string
	outbound                          int
}

var simpleKinds = []simpleKind{
	{"proxy", "proxy", "proxy", "Proxy", "route", throne.OutboundProxy},
	{"direct", "bypass", "direct", "Bypass", "route", throne.OutboundDirect},
	{"block", "block", "block", "Block", "reject", 0},
	{"warp_bypass", "warp_bypass", "warp-bypass", "Warp-bypass", "route", throne.OutboundWarpBypass},
}

// simplePart is the rule of a simple action that holds these attributes.
type simplePart struct {
	kind, title string
	attrs       []string
}

var simpleParts = []simplePart{
	{"address", "Address", []string{"domain", "domain_suffix", "domain_keyword", "domain_regex", "rule_set", "ip_cidr"}},
	{"process_name", "Process Name", []string{"process_name"}},
	{"process_path", "Process Path", []string{"process_path"}},
}

var simplePrefixes = map[string]string{"domain": "domain", "domain_suffix": "suffix", "domain_keyword": "keyword",
	"domain_regex": "regex", "rule_set": "ruleset", "ip_cidr": "ip", "process_name": "processName", "process_path": "processPath"}

func simpleType(k simpleKind, p simplePart) string { return "simple_" + p.kind + "_" + k.suffix }

func simpleKindOf(typ string) (simpleKind, simplePart, bool) {
	for _, k := range simpleKinds {
		for _, p := range simpleParts {
			if simpleType(k, p) == typ {
				return k, p, true
			}
		}
	}
	return simpleKind{}, simplePart{}, false
}

// simpleRule returns a new simple rule of type typ, as Throne makes one.
func simpleRule(typ string) (throne.RouteRule, bool) {
	k, p, ok := simpleKindOf(typ)
	if !ok {
		return throne.RouteRule{}, false
	}
	attrs := map[string]any{"action": k.action}
	if k.action == "route" {
		attrs["outbound"] = k.outbound
	}
	return throne.RouteRule{Type: typ, Name: "Simple " + p.title + " " + k.title, Attributes: attrs}, true
}

func attrKind(name string) (string, bool) {
	switch {
	case name == "outbound":
		return throne.AttrOutbound, true
	case slices.Contains(boolAttributes, name):
		return throne.AttrBool, true
	case selectValues[name] != nil:
		return throne.AttrSelect, true
	case slices.Contains(listAttributes, name):
		return throne.AttrList, true
	case slices.Contains(ruleAttributes, name):
		return throne.AttrText, true
	}
	return "", false
}

func isMatch(name string) bool {
	i := slices.Index(ruleAttributes, name)
	return i >= 0 && i < slices.Index(ruleAttributes, "action")
}

func usesOutbound(action string) bool { return slices.Contains(actionScopes["outbound"], action) }

func routeSchema() throne.RouteSchema {
	attrs := []throne.RuleAttribute{}
	for _, name := range ruleAttributes {
		kind, _ := attrKind(name)
		attrs = append(attrs, throne.RuleAttribute{Name: name, Kind: kind, Match: isMatch(name),
			Values: append([]string{}, selectValues[name]...), Actions: append([]string{}, actionScopes[name]...)})
	}
	return throne.RouteSchema{Attributes: attrs, RuleTypes: slices.Clone(ruleTypes), Outbounds: []throne.RouteOutbound{
		{ID: throne.OutboundProxy, Name: "proxy"}, {ID: throne.OutboundDirect, Name: "direct"},
		{ID: throne.OutboundBlock, Name: "block"}, {ID: throne.OutboundWarpBypass, Name: "warp-bypass"}}}
}

// attrValue reads the value of an attribute of kind as the API writes it, or says what is wrong.
func attrValue(kind string, v any) (any, string) {
	switch kind {
	case throne.AttrList:
		switch x := v.(type) {
		case string:
			if x = strings.TrimSpace(x); x == "" {
				return []string{}, ""
			}
			return []string{x}, ""
		case []any:
			list := []string{}
			for _, item := range x {
				str, isString := item.(string)
				if !isString {
					return nil, "want a list of strings"
				}
				if str = strings.TrimSpace(str); str != "" {
					list = append(list, str)
				}
			}
			return list, ""
		}
		return nil, "want a list of strings"
	case throne.AttrBool:
		switch x := v.(type) {
		case bool:
			return x, ""
		case string:
			if x == "true" || x == "false" {
				return x == "true", ""
			}
		}
		return nil, "want true or false"
	case throne.AttrOutbound:
		if n, isInt := integer(v); isInt {
			return n, ""
		}
		return nil, "want an outbound number or a profile ID"
	}
	switch x := v.(type) {
	case string:
		return x, ""
	case float64:
		if n, isInt := integer(x); isInt {
			return strconv.Itoa(n), ""
		}
	}
	return nil, "want a string"
}

func atDefault(v any) bool {
	switch x := v.(type) {
	case []string:
		return len(x) == 0
	case bool:
		return !x
	case string:
		return x == ""
	}
	return false
}

func dnsHijack() throne.RouteRule {
	return throne.RouteRule{Type: throne.RuleCustom, Name: "dns-hijack", Attributes: map[string]any{"protocol": "dns", "action": "hijack-dns"}}
}

func (s *Server) endpointRule(id int) throne.RouteRule {
	name := "Endpoint"
	if p := s.profiles[id]; p != nil {
		name = p.Name + " route prefer"
	}
	return throne.RouteRule{Type: throne.RuleEndpoint, Name: name, Attributes: map[string]any{"action": "route", "outbound": id}}
}

// parseRule reads the rule at n of a PUT's rules; s.mu is held.
func (s *Server) parseRule(n int, v any) (throne.RouteRule, *response) {
	obj, isObject := v.(map[string]any)
	if !isObject {
		return throne.RouteRule{}, badRequest("rules[%d] must be an object", n)
	}
	typ, _, bad := stringField(obj, "type")
	if bad != nil {
		return throne.RouteRule{}, bad
	}
	name, _, bad := stringField(obj, "name")
	if bad != nil {
		return throne.RouteRule{}, bad
	}
	disabled, bad := boolField(obj, "disabled", false)
	if bad != nil {
		return throne.RouteRule{}, bad
	}
	in, isObject := obj["attributes"].(map[string]any)
	if obj["attributes"] != nil && !isObject {
		return throne.RouteRule{}, badRequest("rules[%d].attributes must be an object", n)
	}
	attrs := map[string]any{}
	for _, k := range slices.Sorted(maps.Keys(in)) {
		kind, known := attrKind(k)
		if !known {
			return throne.RouteRule{}, badRequest("rules[%d]: unknown attribute %s", n, k)
		}
		val, problem := attrValue(kind, in[k])
		if problem != "" {
			return throne.RouteRule{}, badRequest("rules[%d]: %s: %s", n, k, problem)
		}
		attrs[k] = val
	}
	switch {
	case typ == throne.RuleCustom || typ == "":
		rule := throne.RouteRule{Type: throne.RuleCustom, Name: name, Disabled: disabled, Attributes: map[string]any{}}
		if rule.Name == "" {
			rule.Name = fmt.Sprintf("rule_%d", n+1)
		}
		for k, val := range attrs {
			if !atDefault(val) {
				rule.Attributes[k] = val
			}
		}
		action, _ := attrs["action"].(string)
		if action == "" {
			action = "route"
		}
		rule.Attributes["action"] = action
		if _, set := rule.Attributes["outbound"]; !set && usesOutbound(action) {
			rule.Attributes["outbound"] = throne.OutboundDirect
		} else if !usesOutbound(action) {
			delete(rule.Attributes, "outbound")
		}
		return rule, nil
	case typ == throne.RuleEndpoint:
		id, set := attrs["outbound"].(int)
		if !set {
			return throne.RouteRule{}, badRequest("rules[%d]: an endpoint rule needs the outbound attribute, its profile", n)
		}
		rule := s.endpointRule(id)
		rule.Disabled = disabled
		return rule, nil
	}
	rule, known := simpleRule(typ)
	if !known {
		return throne.RouteRule{}, badRequest("rules[%d]: unknown type %q", n, typ)
	}
	_, part, _ := simpleKindOf(typ)
	if name != "" {
		rule.Name = name
	}
	rule.Disabled = disabled
	for k, val := range attrs {
		if slices.Contains(part.attrs, k) && !atDefault(val) {
			rule.Attributes[k] = val
		}
	}
	return rule, nil
}

// simpleText returns the simple rules of k as Throne's window shows them.
func simpleText(rules []throne.RouteRule, k simpleKind) string {
	var b strings.Builder
	for _, r := range rules {
		rk, part, isSimple := simpleKindOf(r.Type)
		if !isSimple || rk.key != k.key {
			continue
		}
		for _, attr := range part.attrs {
			list, _ := r.Attributes[attr].([]string)
			for _, v := range list {
				b.WriteString(simplePrefixes[attr] + ":" + v + "\n")
			}
		}
	}
	return b.String()
}

// updateSimple replaces the simple rules of k with those of text, as Throne's window does.
func updateSimple(rules []throne.RouteRule, k simpleKind, text string) ([]throne.RouteRule, []string) {
	var problems []string
	values := map[string]map[string][]string{} // by rule type, then attribute
	for i, line := range strings.Split(text, "\n") {
		if line = strings.TrimSpace(line); line == "" {
			continue
		}
		prefix, value, _ := strings.Cut(line, ":")
		prefix, value = strings.TrimSpace(prefix), strings.TrimSpace(value)
		attr := ""
		for a, p := range simplePrefixes {
			if p == prefix {
				attr = a
			}
		}
		why := ""
		switch {
		case attr == "":
			why = "Start the line with domain:, suffix:, keyword:, regex:, ruleset:, ip:, processName: or processPath:."
		case value == "":
			why = fmt.Sprintf("Add a value after %q.", prefix+":")
		}
		if why != "" {
			problems = append(problems, fmt.Sprintf("Line %d of the %s rules (%q): %s", i+1, k.label, line, why))
			continue
		}
		if attr == "domain" || attr == "domain_suffix" || attr == "domain_keyword" {
			value = strings.ToLower(value)
		}
		for _, p := range simpleParts {
			if typ := simpleType(k, p); slices.Contains(p.attrs, attr) {
				if values[typ] == nil {
					values[typ] = map[string][]string{}
				}
				values[typ][attr] = append(values[typ][attr], value)
			}
		}
	}
	fill := func(r throne.RouteRule) throne.RouteRule {
		attrs := map[string]any{"action": r.Attributes["action"]}
		if o, set := r.Attributes["outbound"]; set {
			attrs["outbound"] = o
		}
		for a, v := range values[r.Type] {
			attrs[a] = v
		}
		r.Attributes = attrs
		return r
	}
	out, seen := []throne.RouteRule{}, map[string]bool{}
	for _, r := range rules {
		if rk, _, isSimple := simpleKindOf(r.Type); isSimple && rk.key == k.key {
			seen[r.Type] = true
			if len(values[r.Type]) == 0 {
				continue
			}
			r = fill(r)
		}
		out = append(out, r)
	}
	for _, p := range simpleParts {
		if typ := simpleType(k, p); !seen[typ] && len(values[typ]) > 0 {
			r, _ := simpleRule(typ)
			out = append(out, fill(r))
		}
	}
	return out, problems
}

// syncEndpoints keeps an endpoint rule for each endpoint and none for others; s.mu is held.
func (s *Server) syncEndpoints(rules []throne.RouteRule, endpoints []int) []throne.RouteRule {
	out, have := []throne.RouteRule{}, map[int]bool{}
	for _, r := range rules {
		if r.Type == throne.RuleEndpoint {
			id, _ := r.Attributes["outbound"].(int)
			if !slices.Contains(endpoints, id) || have[id] {
				continue
			}
			have[id] = true
		}
		out = append(out, r)
	}
	for _, id := range endpoints {
		if !have[id] {
			out = append(out, s.endpointRule(id))
		}
	}
	return out
}

func cloneRules(rules []throne.RouteRule) []throne.RouteRule {
	out := make([]throne.RouteRule, 0, len(rules))
	for _, r := range rules {
		attrs := make(map[string]any, len(r.Attributes))
		for k, v := range r.Attributes {
			if list, isList := v.([]string); isList {
				v = slices.Clone(list)
			}
			attrs[k] = v
		}
		r.Attributes = attrs
		out = append(out, r)
	}
	return out
}

func intList(f map[string]any, name string) ([]int, bool, *response) {
	v, present := f[name]
	if !present || v == nil {
		return nil, false, nil
	}
	list, isList := v.([]any)
	if !isList {
		return nil, true, badRequest("%s must be a list of profile IDs", name)
	}
	ids := []int{}
	for _, x := range list {
		id, isInt := integer(x)
		if !isInt {
			return nil, true, badRequest("%s must be a list of profile IDs", name)
		}
		if !slices.Contains(ids, id) {
			ids = append(ids, id)
		}
	}
	return ids, true, nil
}

// globalDNS stands in for Throne's own DNS settings.
func globalDNS() throne.RouteDNS {
	return throne.RouteDNS{RemoteDNS: "https://8.8.8.8/dns-query", DirectDNS: "localhost", DNSFinalOut: "remote", EnableDNSRouting: true}
}

// routeDNS lays the dns of a PUT over the stored settings, or the global ones.
func routeDNS(v any, stored *throne.RouteDNS) (*throne.RouteDNS, *response) {
	obj, isObject := v.(map[string]any)
	if !isObject {
		return nil, badRequest("dns must be an object")
	}
	d := globalDNS()
	if stored != nil {
		d = *stored
	}
	strs := map[string]*string{"remote_dns": &d.RemoteDNS, "direct_dns": &d.DirectDNS, "dns_final_out": &d.DNSFinalOut}
	for _, k := range slices.Sorted(maps.Keys(strs)) {
		str, present, bad := stringField(obj, k)
		if bad != nil {
			return nil, badRequest("dns.%s must be a string", k)
		}
		if present {
			*strs[k] = strings.TrimSpace(str)
		}
	}
	bools := map[string]*bool{"remote_dns_disable_ipv6": &d.RemoteDNSDisableIPv6, "direct_dns_disable_ipv6": &d.DirectDNSDisableIPv6,
		"enable_dns_routing": &d.EnableDNSRouting, "fake_dns": &d.FakeDNS, "fakeip_disable_ipv6": &d.FakeIPDisableIPv6}
	for _, k := range slices.Sorted(maps.Keys(bools)) {
		b, bad := boolField(obj, k, *bools[k])
		if bad != nil {
			return nil, badRequest("dns.%s must be true or false", k)
		}
		*bools[k] = b
	}
	return &d, nil
}

func dnsProblem(d throne.RouteDNS) string {
	switch {
	case d.RemoteDNS == "":
		return "Enter the remote DNS server"
	case d.DirectDNS == "":
		return "Enter the direct DNS server"
	case d.DNSFinalOut != "remote" && d.DNSFinalOut != "direct":
		return fmt.Sprintf("The default DNS server must be remote or direct, not %q.", d.DNSFinalOut)
	}
	return ""
}

func (s *Server) validOutbound(id int) bool {
	switch id {
	case throne.OutboundProxy, throne.OutboundDirect, throne.OutboundBlock, throne.OutboundWarpBypass:
		return true
	}
	p := s.profiles[id]
	return id >= 0 && p != nil && p.Type != "extracore"
}

// editRoute applies the fields of a POST or PUT to copies of a routing profile, checks the
// result and returns it, with its warnings; s.mu is held.
func (s *Server) editRoute(rt throne.Route, d routeData, f map[string]any) (throne.Route, *routeData, []string, *response) {
	fault := func(r *response) (throne.Route, *routeData, []string, *response) { return rt, nil, nil, r }
	name, hasName, bad := stringField(f, "name")
	if bad != nil {
		return fault(bad)
	}
	if hasName {
		rt.Name = strings.TrimSpace(name)
	}
	if out, present, bad := intField(f, "default_outbound"); bad != nil {
		return fault(bad)
	} else if present {
		d.defaultOutbound = out
	}
	structured := d.kind != throne.RouteKindRaw
	if v := f["rules"]; v != nil {
		list, isList := v.([]any)
		switch {
		case !structured:
			return fault(badRequest("A raw routing profile has no rules"))
		case !isList:
			return fault(badRequest("rules must be a list of rules"))
		}
		rules := []throne.RouteRule{}
		for i, x := range list {
			r, bad := s.parseRule(i, x)
			if bad != nil {
				return fault(bad)
			}
			rules = append(rules, r)
		}
		d.rules = rules
	}
	var problems, warnings []string
	if v := f["simple"]; v != nil {
		obj, isObject := v.(map[string]any)
		switch {
		case !structured:
			return fault(badRequest("A raw routing profile has no simple rules"))
		case !isObject:
			return fault(badRequest("simple must be an object"))
		}
		for _, k := range simpleKinds {
			text, present, bad := stringField(obj, k.key)
			if bad != nil {
				return fault(badRequest("simple.%s must be a string", k.key))
			}
			if present {
				var p []string
				d.rules, p = updateSimple(d.rules, k, text)
				problems = append(problems, p...)
			}
		}
	}
	for k, p := range map[string]*string{"raw_route": &d.raw, "remote_url": &d.remoteURL} {
		str, present, bad := stringField(f, k)
		if bad != nil {
			return fault(bad)
		}
		if present {
			*p = strings.TrimSpace(str)
		}
	}
	for k, p := range map[string]*bool{"prevent_modifications": &d.prevent, "auto_update": &d.autoUpdate, "custom_dns": &d.customDNS} {
		b, bad := boolField(f, k, *p)
		if bad != nil {
			return fault(bad)
		}
		*p = b
	}
	for k, p := range map[string]*[]int{"endpoints": &d.endpoints, "inner_hop_endpoints": &d.innerHops} {
		ids, present, bad := intList(f, k)
		if bad != nil {
			return fault(bad)
		}
		if present {
			*p = ids
		}
	}
	if v := f["dns"]; v != nil {
		dns, bad := routeDNS(v, d.dns)
		if bad != nil {
			return fault(bad)
		}
		d.dns = dns
	}
	if d.customDNS && d.dns == nil {
		g := globalDNS()
		d.dns = &g
	}
	if structured {
		d.rules = s.syncEndpoints(d.rules, d.endpoints)
	}

	if rt.Name == "" {
		problems = append(problems, "Name: Enter a name")
	}
	if !slices.Contains([]int{throne.OutboundProxy, throne.OutboundDirect, throne.OutboundBlock, throne.OutboundWarpBypass}, d.defaultOutbound) {
		problems = append(problems, "default_outbound: Use -1 (proxy), -2 (direct), -3 (block) or -5 (warp-bypass)")
	}
	for i, r := range d.rules {
		label := fmt.Sprintf("Rule %d (%s)", i+1, r.Name)
		for _, k := range slices.Sorted(maps.Keys(r.Attributes)) {
			str, _ := r.Attributes[k].(string)
			if choices := selectValues[k]; choices != nil && !slices.Contains(choices, str) {
				named := slices.DeleteFunc(slices.Clone(choices), func(c string) bool { return c == "" })
				problems = append(problems, fmt.Sprintf("%s: %s: %q is not one of %s", label, k, str, strings.Join(named, ", ")))
			}
			if id, isInt := r.Attributes[k].(int); k == "outbound" && isInt && !s.validOutbound(id) {
				problems = append(problems, fmt.Sprintf("%s: outbound: no profile %d can take traffic", label, id))
			}
		}
		if r.Type == throne.RuleCustom && !slices.ContainsFunc(slices.Collect(maps.Keys(r.Attributes)), isMatch) {
			warnings = append(warnings, label+": it has no condition, so it applies to every connection")
		}
	}
	var raw map[string]any
	if d.kind == throne.RouteKindRaw && (json.Unmarshal([]byte(d.raw), &raw) != nil || raw == nil) {
		problems = append(problems, "Raw route: not a JSON object")
	}
	switch {
	case d.kind == throne.RouteKindRemote && d.remoteURL == "":
		problems = append(problems, "Remote URL: Enter the URL to download the rules from")
	case d.remoteURL != "" && httpURL(d.remoteURL) != "":
		problems = append(problems, "Remote URL: not an http or https URL")
	}
	for _, id := range d.endpoints {
		if p := s.profiles[id]; p == nil || p.Type != "openvpn" && p.Type != "openconnect" {
			problems = append(problems, fmt.Sprintf("Endpoints: profile %d is not an OpenVPN or OpenConnect profile", id))
		}
	}
	for _, id := range d.innerHops {
		if !slices.Contains(d.endpoints, id) {
			problems = append(problems, fmt.Sprintf("Inner hop endpoints: profile %d is not one of the endpoints", id))
		}
	}
	if d.dns != nil {
		if p := dnsProblem(*d.dns); p != "" {
			problems = append(problems, "DNS: "+p)
		}
	}
	if len(problems) > 0 {
		return fault(fail(422, "invalid_route", "%s\n%s", summary("The routing profile", len(problems)), strings.Join(problems, "\n")))
	}
	if warnings == nil {
		warnings = []string{}
	}
	return rt, &d, warnings, nil
}

func (s *Server) routeIndex(id int) int {
	return slices.IndexFunc(s.routes, func(rt throne.Route) bool { return rt.ID == id })
}

// routeDetail returns a routing profile as GET /v1/routes/{id} does; s.mu is held.
func (s *Server) routeDetail(rt throne.Route) throne.RouteDetail {
	v, d := s.routeView(rt), s.routeData[rt.ID]
	dns := globalDNS()
	if d.dns != nil {
		dns = *d.dns
	}
	return throne.RouteDetail{ID: v.ID, Name: v.Name, Active: v.Active, InUse: v.InUse, SourceGroupID: v.SourceGroupID,
		Kind: d.kind, DefaultOutbound: d.defaultOutbound, Rules: cloneRules(d.rules),
		Simple: throne.SimpleRules{Proxy: simpleText(d.rules, simpleKinds[0]), Direct: simpleText(d.rules, simpleKinds[1]),
			Block: simpleText(d.rules, simpleKinds[2]), WarpBypass: simpleText(d.rules, simpleKinds[3])},
		RawRoute: d.raw, PreventModifications: d.prevent, RemoteURL: d.remoteURL, AutoUpdate: d.autoUpdate,
		RemoteUpdatedAt: d.remoteUpdatedAt, Endpoints: append([]int{}, d.endpoints...),
		InnerHopEndpoints: append([]int{}, d.innerHops...), CustomDNS: d.customDNS, DNS: &dns}
}

func (s *Server) routeChange(rt throne.Route, warnings []string) map[string]any {
	return map[string]any{"route": s.routeDetail(rt), "warnings": warnings, "restart_needed": s.restartNeeded}
}

func (s *Server) getRouteSchema(_ context.Context, _ *request) *response {
	return ok(routeSchema())
}

func (s *Server) getRoute(_ context.Context, r *request) *response {
	s.mu.Lock()
	defer s.mu.Unlock()
	i := s.routeIndex(r.ids[0])
	if i < 0 {
		return notFound("No routing profile %d", r.ids[0])
	}
	return ok(s.routeDetail(s.routes[i]))
}

func (s *Server) postRoute(_ context.Context, r *request) *response {
	kind, _, bad := stringField(r.fields, "kind")
	if kind == "" {
		kind = throne.RouteKindRules
	}
	switch {
	case bad != nil:
		return bad
	case kind == throne.RouteKindRemote:
		return badRequest("A remote routing profile is made in Throne's window")
	case kind != throne.RouteKindRules && kind != throne.RouteKindRaw:
		return badRequest("kind must be rules or raw")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	id := 0
	for _, rt := range s.routes {
		id = max(id, rt.ID+1)
	}
	d := routeData{kind: kind, defaultOutbound: throne.OutboundProxy, rules: []throne.RouteRule{}}
	if kind == throne.RouteKindRules {
		d.rules = []throne.RouteRule{dnsHijack()}
	} else {
		d.raw = `{"final":-1,"rules":[{"domain_suffix":[".example.com"],"outbound":-2}]}`
	}
	rt, nd, warnings, resp := s.editRoute(throne.Route{ID: id, SourceGroupID: -1}, d, r.fields)
	if resp != nil {
		return resp
	}
	s.routes, s.routeData[id] = append(slices.Clone(s.routes), rt), nd
	s.logf("[Routing] Added the routing profile %s", rt.Name)
	s.bump(throne.TopicStatus)
	return &response{status: 201, body: s.routeChange(rt, warnings)}
}

func (s *Server) putRoute(_ context.Context, r *request) *response {
	s.mu.Lock()
	defer s.mu.Unlock()
	i := s.routeIndex(r.ids[0])
	switch {
	case i < 0:
		return notFound("No routing profile %d", r.ids[0])
	case s.change != nil:
		return busy("A profile is starting or stopping")
	}
	rt, d, warnings, resp := s.editRoute(s.routes[i], *s.routeData[r.ids[0]], r.fields)
	if resp != nil {
		return resp
	}
	s.routes[i], s.routeData[rt.ID] = rt, d
	s.logf("[Routing] Saved the routing profile %s", rt.Name)
	if s.state == "running" && s.routeInUse() == rt.ID {
		s.restartNeeded = true
	}
	s.bump(throne.TopicStatus)
	return ok(s.routeChange(rt, warnings))
}

func (s *Server) deleteRoute(_ context.Context, r *request) *response {
	id := r.ids[0]
	s.mu.Lock()
	defer s.mu.Unlock()
	i := s.routeIndex(id)
	switch {
	case i < 0:
		return notFound("No routing profile %d", id)
	case len(s.routes) == 1:
		return fail(409, "last_route", "Throne keeps at least one routing profile")
	case s.change != nil:
		return busy("A profile is starting or stopping")
	}
	inUse := s.state == "running" && s.routeInUse() == id
	rt := s.routes[i]
	s.routes = slices.Delete(slices.Clone(s.routes), i, i+1)
	delete(s.routeData, id)
	if s.activeRoute == id {
		s.activeRoute = s.routes[0].ID
	}
	for _, g := range s.groups {
		if g.routeID == id {
			g.routeID = -1
		}
		if g.id == rt.SourceGroupID {
			g.importRoute = false
		}
	}
	s.restartNeeded = s.restartNeeded || inUse
	s.logf("[Routing] Deleted the routing profile %s", rt.Name)
	s.bump(throne.TopicStatus, throne.TopicGroups)
	return ok(map[string]any{"deleted": id, "restart_needed": s.restartNeeded})
}

// seedRoutes makes up the rules of the routing profiles the fake starts with, and adds a
// remote one; vpn is an OpenVPN profile. s.mu is held.
func (s *Server) seedRoutes(vpn int) {
	lan := throne.RouteRule{Type: throne.RuleCustom, Name: "LAN direct",
		Attributes: map[string]any{"ip_is_private": true, "action": "route", "outbound": throne.OutboundDirect}}
	proxied, _ := simpleRule("simple_address_proxy")
	proxied.Attributes["domain_suffix"] = []string{"example.com", "youtube.com"}
	games, _ := simpleRule("simple_process_name_bypass")
	games.Attributes["process_name"] = []string{"steam"}
	ads, _ := simpleRule("simple_address_block")
	ads.Attributes["domain_suffix"] = []string{"doubleclick.net"}
	raw, _ := json.MarshalIndent(map[string]any{
		"rules": []any{map[string]any{"rule_set": []string{"geoip-ir", "geosite-ir"}, "outbound": throne.OutboundDirect}},
		"final": throne.OutboundProxy,
	}, "", "  ")
	s.routes = append(s.routes, throne.Route{ID: 4, Name: "Community rules", SourceGroupID: -1})
	s.routeData = map[int]*routeData{
		0: {kind: throne.RouteKindRules, defaultOutbound: throne.OutboundProxy, rules: []throne.RouteRule{dnsHijack(), lan, proxied, games}},
		1: {kind: throne.RouteKindRules, defaultOutbound: throne.OutboundProxy, endpoints: []int{vpn}, rules: []throne.RouteRule{
			dnsHijack(), lan,
			{Type: throne.RuleCustom, Name: "China direct", Attributes: map[string]any{"rule_set": []string{"geosite-cn", "geoip-cn"},
				"action": "route", "outbound": throne.OutboundDirect}},
			{Type: throne.RuleCustom, Name: "Block ads", Attributes: map[string]any{"rule_set": []string{"geosite-category-ads-all"},
				"action": "reject", "method": "drop"}},
			ads, s.endpointRule(vpn),
		}},
		2: {kind: throne.RouteKindRules, defaultOutbound: throne.OutboundProxy, rules: []throne.RouteRule{dnsHijack()}, customDNS: true,
			dns: &throne.RouteDNS{RemoteDNS: "https://1.1.1.1/dns-query", DirectDNS: "localhost", DNSFinalOut: "remote", EnableDNSRouting: true}},
		3: {kind: throne.RouteKindRaw, defaultOutbound: throne.OutboundProxy, rules: []throne.RouteRule{}, raw: string(raw)},
		4: {kind: throne.RouteKindRemote, defaultOutbound: throne.OutboundProxy, autoUpdate: true,
			remoteURL: "https://raw.githubusercontent.com/example/throne-rules/main/route.json", remoteUpdatedAt: time.Now().Unix() - 2*86400,
			rules: []throne.RouteRule{dnsHijack(), {Type: throne.RuleCustom, Name: "Telegram via proxy", Attributes: map[string]any{
				"domain_suffix": []string{"t.me", "telegram.org"}, "ip_cidr": []string{"91.108.4.0/22", "149.154.160.0/20"},
				"action": "route", "outbound": throne.OutboundProxy}}}},
	}
}
