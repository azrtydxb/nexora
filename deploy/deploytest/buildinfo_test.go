package deploytest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDockerfilesStampBuildInfo(t *testing.T) {
	root := filepath.Join("..", "..")
	read := func(p string) string {
		b, err := os.ReadFile(filepath.Join(root, p))
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	for file, wants := range map[string][]string{
		"deploy/docker/mgmt.Dockerfile":   {"ARG VERSION=dev", "ARG COMMIT=", "ARG BUILD_DATE=", "-X main.commit=${COMMIT}", "-X main.buildDate=${BUILD_DATE}", "NEXORA_COMMIT=${COMMIT}"},
		"deploy/docker/engine.Dockerfile": {"ARG COMMIT=", `NEXORA_COMMIT="${COMMIT}"`},
		"scripts/build-image.sh":          {"build-arg:COMMIT=", "build-arg:BUILD_DATE="},
		".github/workflows/images.yml":    {"COMMIT=${{ github.sha }}", "BUILD_DATE="},
		"web/vite.config.ts":              {"__NEXORA_VERSION__", "__NEXORA_COMMIT__", "__NEXORA_BUILD_DATE__"},
	} {
		body := read(file)
		for _, w := range wants {
			if !strings.Contains(body, w) {
				t.Errorf("%s lacks %q", file, w)
			}
		}
	}
}
