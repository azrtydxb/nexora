// Command genhex writes <zone>.hex next to each RFC 8976 vector: one RR per line, the lowercase
// hex of its uncompressed wire form, in file order. Rust tests read these files.
package main

import (
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/miekg/dns"
)

func main() {
	dir := os.Args[1]
	for _, name := range []string{"a1", "a2", "a3"} {
		src, err := os.ReadFile(filepath.Join(dir, name+".zone"))
		if err != nil {
			panic(err)
		}
		zp := dns.NewZoneParser(strings.NewReader(string(src)), "example.", name+".zone")
		var lines []string
		for rr, ok := zp.Next(); ok; rr, ok = zp.Next() {
			buf := make([]byte, 65535)
			n, err := dns.PackRR(rr, buf, 0, nil, false)
			if err != nil {
				panic(err)
			}
			lines = append(lines, hex.EncodeToString(buf[:n]))
		}
		if err := zp.Err(); err != nil {
			panic(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name+".hex"), []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
			panic(err)
		}
		fmt.Println(name, len(lines), "records")
	}
}
