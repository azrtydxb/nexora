package main

import (
	"bytes"
	"io"
	"testing"

	"github.com/miekg/dns"
)

func TestFrames(t *testing.T) {
	for _, size := range []int{1, 512, 65535} {
		payload := bytes.Repeat([]byte{42}, size)
		var buf bytes.Buffer
		if err := framedWrite(&buf, payload); err != nil {
			t.Fatal(err)
		}
		got, err := framedRead(&buf)
		if err != nil || !bytes.Equal(got, payload) {
			t.Fatalf("size %d: %v", size, err)
		}
	}
	for _, size := range []int{0, 65536} {
		if err := framedWrite(io.Discard, make([]byte, size)); err == nil {
			t.Fatalf("accepted size %d", size)
		}
	}
	for _, raw := range [][]byte{{0}, {0, 4, 1, 2}} {
		if _, err := framedRead(bytes.NewReader(raw)); err == nil {
			t.Fatal("accepted truncated frame")
		}
	}
}

func TestSocketAttribution(t *testing.T) {
	q := new(dns.Msg)
	q.SetQuestion("udp.dsr-lab.test.", dns.TypeTXT)
	r := answer(q, "198.18.0.10:23456")
	if !r.Response || r.Id != q.Id || r.Question[0] != q.Question[0] || r.Answer[0].(*dns.TXT).Txt[0] != "198.18.0.10" {
		t.Fatalf("%v", r)
	}
	if answer(q, "bad-peer").Rcode != dns.RcodeServerFailure {
		t.Fatal("accepted invalid peer")
	}
	q.Question = nil
	if answer(q, "198.18.0.10:23456").Rcode != dns.RcodeFormatError {
		t.Fatal("accepted missing question")
	}
}
