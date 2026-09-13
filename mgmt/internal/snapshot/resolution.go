package snapshot

import (
	"sort"
	"time"

	controlv1 "github.com/piwi3910/nexora/gen/go/nexora/control/v1"
	"github.com/piwi3910/nexora/mgmt/internal/store"
)

var (
	rpzOverrides = map[string]controlv1.RpzPolicyOverride{
		"given":    controlv1.RpzPolicyOverride_RPZ_POLICY_OVERRIDE_GIVEN,
		"disabled": controlv1.RpzPolicyOverride_RPZ_POLICY_OVERRIDE_DISABLED,
		"nxdomain": controlv1.RpzPolicyOverride_RPZ_POLICY_OVERRIDE_NXDOMAIN,
		"nodata":   controlv1.RpzPolicyOverride_RPZ_POLICY_OVERRIDE_NODATA,
		"passthru": controlv1.RpzPolicyOverride_RPZ_POLICY_OVERRIDE_PASSTHRU,
		"drop":     controlv1.RpzPolicyOverride_RPZ_POLICY_OVERRIDE_DROP,
		"tcp_only": controlv1.RpzPolicyOverride_RPZ_POLICY_OVERRIDE_TCP_ONLY,
	}
	tsigAlgorithms = map[string]controlv1.TsigAlgorithm{
		"hmac-sha256": controlv1.TsigAlgorithm_TSIG_ALGORITHM_HMAC_SHA256,
		"hmac-sha512": controlv1.TsigAlgorithm_TSIG_ALGORITHM_HMAC_SHA512,
	}
)

// ApplyResolution fills the M3 snapshot sections from rows: resolution mode and recursion
// settings, forward zones, DNSSEC (NTAs expired at now omitted) and RPZ zones in policy order (file
// zones without an uploaded file omitted). TSIG secret envelopes are never read: secrets reach
// engines only as RpzTsigKeys.
func ApplyResolution(s *controlv1.ConfigSnapshot, rows store.ResolutionRows, now time.Time) {
	res := rows.Resolution
	s.ResolutionMode = controlv1.ResolutionMode_RESOLUTION_MODE_FORWARD
	if res.Mode == "recursive" {
		s.ResolutionMode = controlv1.ResolutionMode_RESOLUTION_MODE_RECURSIVE
	}
	s.Recursion = &controlv1.RecursionConfig{
		QnameMinimisation:  res.QnameMinimisation,
		AggressiveNsec:     res.AggressiveNSEC,
		MaxUpstreamQueries: uint32(res.MaxUpstreamQueries),
		MaxDelegationDepth: uint32(res.MaxDelegationDepth),
		AuthorityPort:      uint32(res.AuthorityPort),
	}
	for _, h := range res.RootHints {
		s.Recursion.RootHints = append(s.Recursion.RootHints, &controlv1.RootHint{Name: h.Name, Addresses: append([]string(nil), h.Addresses...)})
	}

	s.ForwardZones = nil
	for _, z := range rows.ForwardZones {
		s.ForwardZones = append(s.ForwardZones, &controlv1.ForwardZone{Domain: z.Domain, Addresses: append([]string(nil), z.Addresses...), Validate: z.Validate})
	}

	s.Dnssec = &controlv1.DnssecConfig{Validation: rows.Dnssec.Validation, Rfc5011: rows.Dnssec.RFC5011}
	for _, a := range rows.Anchors {
		s.Dnssec.TrustAnchors = append(s.Dnssec.TrustAnchors, &controlv1.TrustAnchor{Zone: a.Zone, Ds: a.DS})
	}
	for _, n := range rows.NTAs {
		if n.ExpiresAt.After(now) {
			s.Dnssec.NegativeTrustAnchors = append(s.Dnssec.NegativeTrustAnchors, &controlv1.NegativeTrustAnchor{Domain: n.Domain, ExpiresUnix: n.ExpiresAt.Unix()})
		}
	}
	// The engine rejects a snapshot that validates forwarded answers without validation.
	s.DnssecValidateForwarded = rows.Dnssec.Validation && rows.Dnssec.ValidateForwarded

	zones := append([]store.RPZZone(nil), rows.RPZ...)
	sort.SliceStable(zones, func(i, j int) bool { return zones[i].Position < zones[j].Position })
	s.RpzZones = nil
	for _, z := range zones {
		out := &controlv1.RpzZone{Id: z.ID.String(), Name: z.Name, PolicyOverride: rpzOverrides[z.PolicyOverride], RefreshNonce: uint64(z.RefreshNonce)}
		switch z.SourceType {
		case "file":
			if z.BlobSHA256 == nil {
				continue
			}
			out.Source = &controlv1.RpzZone_File{File: &controlv1.RpzFileSource{
				Blob: &controlv1.BlobRef{Sha256: *z.BlobSHA256, Size: uint64(z.BlobSize), Name: z.Name},
			}}
		case "transfer":
			tr := &controlv1.RpzTransferSource{Primary: deref(z.PrimaryAddress), TsigKeyName: deref(z.TSIGKeyName), MinRefreshSeconds: uint32(z.MinRefreshSeconds)}
			if z.TSIGAlgorithm != nil {
				tr.TsigAlgorithm = tsigAlgorithms[*z.TSIGAlgorithm]
			}
			out.Source = &controlv1.RpzZone_Transfer{Transfer: tr}
		default:
			continue
		}
		s.RpzZones = append(s.RpzZones, out)
	}
}

func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}
