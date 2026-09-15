package mcpserver

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// maxReplyBytes bounds one HTTP reply the bridge relays (a 1 MiB result can grow when JSON-escaped).
const maxReplyBytes = 16 << 20

// RunStdio bridges newline-delimited JSON-RPC on in/out to the Streamable HTTP endpoint <baseURL>/mcp,
// authenticating every message with the API token. It has no store access; the endpoint enforces
// authentication, RBAC and read-only mode. It returns when in ends or ctx is done.
func RunStdio(ctx context.Context, in io.Reader, out io.Writer, baseURL, token string, client *http.Client) error {
	endpoint := strings.TrimSuffix(baseURL, "/") + "/mcp"
	sc := bufio.NewScanner(in)
	sc.Buffer(make([]byte, 0, 64<<10), maxRequestBytes)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		reply, err := forward(ctx, client, endpoint, token, line)
		if err != nil {
			return err
		}
		if reply == nil {
			continue
		}
		if _, err := out.Write(append(reply, '\n')); err != nil {
			return err
		}
	}
	return sc.Err()
}

// forward posts one message and returns the line to write back, or nil when there is none (a
// notification). An HTTP or transport failure of a request becomes a JSON-RPC error for its id.
func forward(ctx context.Context, client *http.Client, endpoint, token string, msg []byte) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(msg))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return failure(msg, err.Error()), nil
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxReplyBytes+1))
	switch {
	case err != nil:
		return failure(msg, "read reply: "+err.Error()), nil
	case len(body) > maxReplyBytes:
		return failure(msg, fmt.Sprintf("reply exceeds %d bytes", maxReplyBytes)), nil
	case resp.StatusCode == http.StatusAccepted:
		return nil, nil
	case resp.StatusCode != http.StatusOK:
		return failure(msg, fmt.Sprintf("HTTP %d: %s", resp.StatusCode, bytes.TrimSpace(body))), nil
	}
	var line bytes.Buffer
	if err := json.Compact(&line, body); err != nil {
		return failure(msg, "invalid reply: "+err.Error()), nil
	}
	return line.Bytes(), nil
}

// failure is a JSON-RPC internal error for msg's id, or nil when msg has no id.
func failure(msg []byte, message string) []byte {
	var m struct {
		ID json.RawMessage `json:"id"`
	}
	if json.Unmarshal(msg, &m) != nil || len(m.ID) == 0 || string(m.ID) == "null" {
		return nil
	}
	out, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": m.ID, "error": rpcError{codeInternal, message}})
	return out
}
