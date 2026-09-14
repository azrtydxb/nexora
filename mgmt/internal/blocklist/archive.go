package blocklist

import (
	"archive/tar"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"strings"
)

// ReadArchiveMember parses only `member` of a gzip-compressed tar archive (a UT1 blacklists.tar.gz),
// reading at most MaxListBytes of it.
func ReadArchiveMember(r io.Reader, member string) ([]string, ParseStats, error) {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return nil, ParseStats{}, fmt.Errorf("read archive: %v", err)
	}
	defer func() { _ = gz.Close() }()
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil, ParseStats{}, fmt.Errorf("archive member %s not found", member)
		}
		if err != nil {
			return nil, ParseStats{}, fmt.Errorf("read archive: %v", err)
		}
		if h.Typeflag != tar.TypeReg || strings.TrimPrefix(h.Name, "./") != member {
			continue
		}
		body := &io.LimitedReader{R: tr, N: MaxListBytes + 1}
		domains, stats, err := Parse(body)
		if body.N == 0 {
			return nil, ParseStats{}, errors.New("archive member exceeds 256 MiB")
		}
		if err != nil {
			return nil, ParseStats{}, fmt.Errorf("read archive: %v", err)
		}
		return domains, stats, nil
	}
}
