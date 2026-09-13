package api

import (
	"errors"
	"fmt"
	"net/http"
	"net/netip"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
	"golang.org/x/net/idna"

	"github.com/piwi3910/nexora/mgmt/internal/store"
)

var (
	// policyDomainRE matches the policy tables' CHECK constraints (lowercase punycode, no trailing dot).
	policyDomainRE = regexp.MustCompile(`^([a-z0-9_]([a-z0-9_-]{0,61}(?:[a-z0-9_]))?\.)*[a-z0-9_]([a-z0-9_-]{0,61}(?:[a-z0-9_]))?$`)
	groupNameRE    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9 _.-]{0,62}$`)
)

// unprocessable is a policy request rejected with 422 validation_failed.
type unprocessable struct{ msg string }

func (e unprocessable) Error() string { return e.msg }

func failed(format string, args ...any) error {
	return unprocessable{msg: fmt.Sprintf(format, args...)}
}

// policyFailure classifies err into the 409/422 answers M1's mapError has no case for. ok is false
// for every other error, which the caller returns for mapError.
func policyFailure(err error) (status int, body Error, ok bool) {
	var u unprocessable
	switch {
	case errors.As(err, &u):
		return http.StatusUnprocessableEntity, Error{Code: "validation_failed", Message: u.msg}, true
	case errors.Is(err, store.ErrUnknownFilterList):
		return http.StatusUnprocessableEntity, Error{Code: "validation_failed", Message: "filter_list_ids must name existing block lists"}, true
	case errors.Is(err, store.ErrUnknownGroup):
		return http.StatusUnprocessableEntity, Error{Code: "validation_failed", Message: "group_id names no policy group"}, true
	case errors.Is(err, store.ErrCIDRInUse):
		return http.StatusConflict, Error{Code: "cidr_in_use", Message: err.Error()}, true
	case errors.Is(err, store.ErrRewriteCNAMEConflict):
		return http.StatusConflict, Error{Code: "rewrite_conflict", Message: err.Error()}, true
	}
	return 0, Error{}, false
}

// normalDomain trims, strips one trailing dot, converts IDNs to punycode and lowercases.
func normalDomain(in string) (string, bool) {
	d := strings.TrimSuffix(strings.TrimSpace(in), ".")
	for i := 0; i < len(d); i++ {
		if d[i] >= utf8.RuneSelf {
			// Only non-ASCII input goes through IDNA: its STD3 rules would refuse the underscores
			// that ASCII service names use and the tables allow.
			a, err := idna.Lookup.ToASCII(d)
			if err != nil {
				return "", false
			}
			d = a
			break
		}
	}
	d = strings.ToLower(d)
	return d, len(d) <= 253 && policyDomainRE.MatchString(d)
}

func validateSafeSearch(in *SafeSearch) (store.SafeSearch, error) {
	if in == nil {
		return store.SafeSearch{YouTube: "off"}, nil
	}
	return safeSearchFields(in.Google, in.Bing, in.Duckduckgo, string(in.Youtube))
}

func safeSearchFields(google, bing, ddg bool, youtube string) (store.SafeSearch, error) {
	switch youtube {
	case "":
		youtube = "off"
	case "off", "moderate", "strict":
	default:
		return store.SafeSearch{}, failed("youtube must be off, moderate or strict")
	}
	return store.SafeSearch{Google: google, Bing: bing, DuckDuckGo: ddg, YouTube: youtube}, nil
}

func validatePolicyGroup(in PolicyGroupInput) (store.PolicyGroup, error) {
	g := store.PolicyGroup{Name: in.Name}
	if !groupNameRE.MatchString(in.Name) {
		return g, failed("name must match ^[A-Za-z0-9][A-Za-z0-9 _.-]{0,62}$")
	}
	if in.Description != nil {
		g.Description = *in.Description
	}
	if utf8.RuneCountInString(g.Description) > 500 {
		return g, failed("description must be at most 500 characters")
	}
	if len(in.Cidrs) < 1 || len(in.Cidrs) > 1024 {
		return g, failed("cidrs must list 1 to 1024 prefixes")
	}
	seen := map[netip.Prefix]bool{}
	for _, c := range in.Cidrs {
		p, err := netip.ParsePrefix(strings.TrimSpace(c))
		if err != nil {
			return g, failed("cidr %s is invalid", c)
		}
		if p != p.Masked() {
			return g, failed("cidr %s has host bits set", c)
		}
		if seen[p] {
			return g, failed("cidr %s listed twice", p)
		}
		seen[p] = true
		g.CIDRs = append(g.CIDRs, p)
	}
	if in.FilterListIds != nil {
		if len(*in.FilterListIds) > 256 {
			return g, failed("filter_list_ids must list at most 256 lists")
		}
		ids := map[uuid.UUID]bool{}
		for _, id := range *in.FilterListIds {
			if !ids[id] {
				ids[id] = true
				g.FilterListIDs = append(g.FilterListIDs, id)
			}
		}
	}
	if in.Allowlist != nil {
		if len(*in.Allowlist) > 10000 {
			return g, failed("allowlist must list at most 10000 domains")
		}
		domains := map[string]bool{}
		for _, raw := range *in.Allowlist {
			d, ok := normalDomain(raw)
			if !ok {
				return g, failed("domain %s is invalid", raw)
			}
			if !domains[d] {
				domains[d] = true
				g.Allowlist = append(g.Allowlist, d)
			}
		}
	}
	var err error
	g.SafeSearch, err = validateSafeSearch(in.SafeSearch)
	return g, err
}

func validateRewrite(in RewriteInput) (store.Rewrite, error) {
	r := store.Rewrite{GroupID: in.GroupId, Type: string(in.Type), TTL: 300}
	name, wildcard := strings.TrimSpace(in.Name), false
	if rest, ok := strings.CutPrefix(name, "*."); ok {
		name, wildcard = rest, true
	}
	n, ok := normalDomain(name)
	if !ok {
		return r, failed("name %s is not a domain or *.domain", in.Name)
	}
	if wildcard {
		n = "*." + n
	}
	r.Name = n
	value := strings.TrimSpace(in.Value)
	switch in.Type {
	case RewriteInputTypeA:
		a, err := netip.ParseAddr(value)
		if err != nil || !a.Is4() {
			return r, failed("value %s is not an IPv4 address", in.Value)
		}
		r.Value = a.String()
	case RewriteInputTypeAAAA:
		a, err := netip.ParseAddr(value)
		if err != nil || !a.Is6() || a.Is4In6() || a.Zone() != "" {
			return r, failed("value %s is not an IPv6 address", in.Value)
		}
		r.Value = a.String()
	case RewriteInputTypeCNAME:
		target, ok := normalDomain(value)
		if !ok {
			return r, failed("domain %s is invalid", in.Value)
		}
		if target == r.Name {
			return r, failed("CNAME target must differ from name")
		}
		r.Value = target
	default:
		return r, failed("type must be A, AAAA or CNAME")
	}
	if in.Ttl != nil {
		if *in.Ttl < 0 || *in.Ttl > 86400 {
			return r, failed("ttl must be between 0 and 86400")
		}
		r.TTL = int32(*in.Ttl)
	}
	return r, nil
}

// rewriteScope parses listRewrites' scope: all (default), global, or a policy group id.
func rewriteScope(scope *string) (groupID *uuid.UUID, all bool, err error) {
	if scope == nil || *scope == "" || *scope == "all" {
		return nil, true, nil
	}
	if *scope == "global" {
		return nil, false, nil
	}
	id, perr := uuid.Parse(*scope)
	if perr != nil {
		return nil, false, failed("scope must be all, global or a policy group id")
	}
	return &id, false, nil
}
