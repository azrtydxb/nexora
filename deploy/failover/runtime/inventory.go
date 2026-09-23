package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"os/exec"
	"runtime"
	"time"
)

// This inventory is deliberately NOT a production topology proof. It admits
// only two veth pairs with BOTH ends in the calling namespace, no IP addresses
// on any veth, and a loopback-only API server. Thus ARM cannot expose a service.
// Management test frames use the separate mg0/mp0 pair, never the gated pair.
func labInventory(ctx context.Context, cfg Config) error {
	u, e := url.Parse(cfg.Authority.Endpoint)
	if e != nil || u.Scheme != "https" || u.Hostname() != "127.0.0.1" || cfg.Driver.Interface != "fg0" || runtime.GOOS != "linux" {
		return errors.New("detached lab requires fg0 and explicit loopback HTTPS API origin")
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "/usr/sbin/ip", "-j", "-d", "address", "show")
	cmd.Env = []string{"LC_ALL=C"}
	cmd.WaitDelay = time.Second
	var output boundedBuffer
	cmd.Stdout = &output
	if cmd.Run() != nil || ctx.Err() != nil {
		return errors.New("lab transmission inventory failed")
	}
	return validateInventory(output.Bytes())
}

type boundedBuffer struct{ bytes.Buffer }

func (b *boundedBuffer) Write(p []byte) (int, error) {
	if b.Len()+len(p) > 64<<10 {
		return 0, errors.New("inventory exceeds bound")
	}
	return b.Buffer.Write(p)
}

func validateInventory(data []byte) error {
	var links []struct {
		Name     string          `json:"ifname"`
		LinkType string          `json:"link_type"`
		Index    int             `json:"ifindex"`
		Peer     int             `json:"link_index"`
		PeerName string          `json:"link"`
		NS       json.RawMessage `json:"link_netnsid"`
		Master   json.RawMessage `json:"master"`
		Info     struct {
			Kind string `json:"info_kind"`
		} `json:"linkinfo"`
		Addresses []struct {
			Local string `json:"local"`
		} `json:"addr_info"`
	}
	if json.Unmarshal(data, &links) != nil || len(links) != 5 {
		return errors.New("exact detached five-link inventory required")
	}
	indices := map[string]int{}
	peers := map[string]int{}
	peerNames := map[string]string{}
	seen := map[int]bool{}
	for _, l := range links {
		if l.Index <= 0 || seen[l.Index] || indices[l.Name] != 0 || len(l.NS) != 0 || len(l.Master) != 0 {
			return errors.New("foreign or duplicate lab link")
		}
		seen[l.Index] = true
		indices[l.Name] = l.Index
		peers[l.Name] = l.Peer
		peerNames[l.Name] = l.PeerName
		if l.Name == "lo" {
			if l.LinkType != "loopback" {
				return errors.New("real loopback link required")
			}
			for _, a := range l.Addresses {
				if a.Local != "127.0.0.1" && a.Local != "::1" {
					return errors.New("foreign loopback address")
				}
			}
		} else {
			if l.Name != "fg0" && l.Name != "fp0" && l.Name != "mg0" && l.Name != "mp0" {
				return errors.New("unaccounted transmission path")
			}
			if l.Info.Kind != "veth" || len(l.Addresses) != 0 {
				return errors.New("lab veth must be addressless")
			}
		}
	}
	// iproute2 emits a local peer's name instead of link_index when it can
	// resolve that name. Resolve only within this exact inventory; if both
	// representations are supplied they must identify the same local peer.
	for name, peerName := range peerNames {
		if peerName == "" {
			continue
		}
		index := indices[peerName]
		if index == 0 || (peers[name] != 0 && peers[name] != index) {
			return errors.New("unknown or conflicting local peer identity")
		}
		peers[name] = index
	}
	for _, pair := range [][2]string{{"fg0", "fp0"}, {"mg0", "mp0"}} {
		if indices[pair[0]] == 0 || indices[pair[1]] == 0 || peers[pair[0]] != indices[pair[1]] || peers[pair[1]] != indices[pair[0]] {
			return errors.New("lab peers must both remain local; external egress refused")
		}
	}
	if indices["lo"] == 0 {
		return errors.New("loopback missing")
	}
	return nil
}
