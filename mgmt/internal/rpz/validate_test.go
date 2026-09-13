package rpz

import (
	"bytes"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/klauspost/compress/zstd"
)

const zone = `$TTL 60
@ SOA ns.rpz. hostmaster.rpz. 7 60 60 86400 60
@ NS ns.rpz.
bad.example CNAME .
32.66.2.0.192.rpz-ip CNAME .
local.example A 10.9.9.9
`

func TestValidateZoneCountsRecordsAndSerial(t *testing.T) {
	s, err := ValidateZone("rpz.file.test.", zone)
	if err != nil {
		t.Fatalf("ValidateZone: %v", err)
	}
	if s.Serial != 7 || s.Records != 3 {
		t.Fatalf("summary = %+v, want serial 7 records 3", s)
	}
}

func TestValidateZoneRejectsIncludeMissingSOASyntaxAndSize(t *testing.T) {
	for name, text := range map[string]string{
		"include": "$INCLUDE /etc/passwd\n" + zone,
		"no soa":  "$TTL 60\nbad.example CNAME .\n",
		"syntax":  zone + "broken.example IN A not-an-ip\n",
		"size":    zone + strings.Repeat("; padding\n", MaxZoneBytes/10+1),
	} {
		if _, err := ValidateZone("rpz.file.test.", text); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
	_, err := ValidateZone("rpz.file.test.", "$INCLUDE x\n")
	if err == nil || !strings.Contains(err.Error(), "$INCLUDE is not allowed") {
		t.Fatalf("include error = %v", err)
	}
}

func TestPackIsZstdOfContentAndPurposeNamesZone(t *testing.T) {
	d, _ := zstd.NewReader(nil)
	out, err := d.DecodeAll(Pack(zone), nil)
	if err != nil || !bytes.Equal(out, []byte(zone)) {
		t.Fatalf("round trip failed: %v", err)
	}
	if !bytes.Equal(Pack(zone), Pack(zone)) {
		t.Fatal("Pack is not deterministic")
	}
	id := uuid.MustParse("11111111-1111-1111-1111-111111111111")
	if TsigPurpose(id) != "nexora/rpz-tsig/v1:11111111-1111-1111-1111-111111111111" {
		t.Fatalf("purpose = %q", TsigPurpose(id))
	}
}
