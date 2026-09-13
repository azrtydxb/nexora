package snapshot

import (
	controlv1 "github.com/piwi3910/nexora/gen/go/nexora/control/v1"
	"github.com/piwi3910/nexora/mgmt/internal/store"
)

// Safe-search rewrite set ids, shared by every scope that enables the provider.
const (
	SafeSearchGoogle          = "safesearch:google"
	SafeSearchBing            = "safesearch:bing"
	SafeSearchDuckDuckGo      = "safesearch:duckduckgo"
	SafeSearchYouTubeStrict   = "safesearch:youtube-strict"
	SafeSearchYouTubeModerate = "safesearch:youtube-moderate"
	safeSearchTTL             = 300
)

// googleDomains is https://www.google.com/supported_domains (fetched 2026-09-13).
var googleDomains = []string{
	"google.com", "google.ad", "google.ae", "google.com.af", "google.com.ag", "google.al", "google.am", "google.co.ao",
	"google.com.ar", "google.as", "google.at", "google.com.au", "google.az", "google.ba", "google.com.bd", "google.be",
	"google.bf", "google.bg", "google.com.bh", "google.bi", "google.bj", "google.com.bn", "google.com.bo", "google.com.br",
	"google.bs", "google.bt", "google.co.bw", "google.by", "google.com.bz", "google.ca", "google.cd", "google.cf",
	"google.cg", "google.ch", "google.ci", "google.co.ck", "google.cl", "google.cm", "google.cn", "google.com.co",
	"google.co.cr", "google.com.cu", "google.cv", "google.com.cy", "google.cz", "google.de", "google.dj", "google.dk",
	"google.dm", "google.com.do", "google.dz", "google.com.ec", "google.ee", "google.com.eg", "google.es", "google.com.et",
	"google.fi", "google.com.fj", "google.fm", "google.fr", "google.ga", "google.ge", "google.gg", "google.com.gh",
	"google.com.gi", "google.gl", "google.gm", "google.gr", "google.com.gt", "google.gy", "google.com.hk", "google.hn",
	"google.hr", "google.ht", "google.hu", "google.co.id", "google.ie", "google.co.il", "google.im", "google.co.in",
	"google.iq", "google.is", "google.it", "google.je", "google.com.jm", "google.jo", "google.co.jp", "google.co.ke",
	"google.com.kh", "google.ki", "google.kg", "google.co.kr", "google.com.kw", "google.kz", "google.la", "google.com.lb",
	"google.li", "google.lk", "google.co.ls", "google.lt", "google.lu", "google.lv", "google.com.ly", "google.co.ma",
	"google.md", "google.me", "google.mg", "google.mk", "google.ml", "google.com.mm", "google.mn", "google.com.mt",
	"google.mu", "google.mv", "google.mw", "google.com.mx", "google.com.my", "google.co.mz", "google.com.na", "google.com.ng",
	"google.com.ni", "google.ne", "google.nl", "google.no", "google.com.np", "google.nr", "google.nu", "google.co.nz",
	"google.com.om", "google.com.pa", "google.com.pe", "google.com.pg", "google.com.ph", "google.com.pk", "google.pl", "google.pn",
	"google.com.pr", "google.ps", "google.pt", "google.com.py", "google.com.qa", "google.ro", "google.ru", "google.rw",
	"google.com.sa", "google.com.sb", "google.sc", "google.se", "google.com.sg", "google.sh", "google.si", "google.sk",
	"google.com.sl", "google.sn", "google.so", "google.sm", "google.sr", "google.st", "google.com.sv", "google.td",
	"google.tg", "google.co.th", "google.com.tj", "google.tl", "google.tm", "google.tn", "google.to", "google.com.tr",
	"google.tt", "google.com.tw", "google.co.tz", "google.com.ua", "google.co.ug", "google.co.uk", "google.com.uy", "google.co.uz",
	"google.com.vc", "google.co.ve", "google.co.vi", "google.com.vn", "google.vu", "google.ws", "google.rs", "google.co.za",
	"google.co.zm", "google.co.zw", "google.cat",
}

var youtubeNames = []string{"www.youtube.com", "m.youtube.com", "youtubei.googleapis.com", "youtube.googleapis.com", "www.youtube-nocookie.com"}

func cnameSet(id, label, target string, names []string) *controlv1.RewriteSet {
	s := &controlv1.RewriteSet{Id: id, Label: label}
	for _, n := range names {
		s.Rules = append(s.Rules, &controlv1.RewriteRule{Name: n, Type: controlv1.RewriteType_REWRITE_TYPE_CNAME, Value: target, Ttl: safeSearchTTL})
	}
	return s
}

// SafeSearchSet returns the provider rewrite set for id, or nil for an unknown id.
func SafeSearchSet(id string) *controlv1.RewriteSet {
	switch id {
	case SafeSearchGoogle:
		names := make([]string, 0, 2*len(googleDomains))
		for _, d := range googleDomains {
			names = append(names, "www."+d, d)
		}
		return cnameSet(id, "Google SafeSearch", "forcesafesearch.google.com", names)
	case SafeSearchBing:
		return cnameSet(id, "Bing SafeSearch", "strict.bing.com", []string{"www.bing.com", "bing.com"})
	case SafeSearchDuckDuckGo:
		return cnameSet(id, "DuckDuckGo safe search", "safe.duckduckgo.com", []string{"duckduckgo.com", "www.duckduckgo.com", "start.duckduckgo.com"})
	case SafeSearchYouTubeStrict:
		return cnameSet(id, "YouTube Restricted (strict)", "restrict.youtube.com", youtubeNames)
	case SafeSearchYouTubeModerate:
		return cnameSet(id, "YouTube Restricted (moderate)", "restrictmoderate.youtube.com", youtubeNames)
	}
	return nil
}

func safeSearchIDs(s store.SafeSearch) []string {
	var ids []string
	if s.Google {
		ids = append(ids, SafeSearchGoogle)
	}
	if s.Bing {
		ids = append(ids, SafeSearchBing)
	}
	if s.DuckDuckGo {
		ids = append(ids, SafeSearchDuckDuckGo)
	}
	switch s.YouTube {
	case "strict":
		ids = append(ids, SafeSearchYouTubeStrict)
	case "moderate":
		ids = append(ids, SafeSearchYouTubeModerate)
	}
	return ids
}
