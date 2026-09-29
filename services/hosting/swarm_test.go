// Copyright 2026 The GitW3 Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package hosting

import (
	"encoding/binary"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// dockerFrame wraps a line in Docker's multiplexed stream framing.
func dockerFrame(stream byte, line string) []byte {
	header := make([]byte, 8)
	header[0] = stream
	binary.BigEndian.PutUint32(header[4:], uint32(len(line)))
	return append(header, line...)
}

func TestSwarmLogsExactServiceWithTimestamps(t *testing.T) {
	var query string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/services":
			// Docker's name filter matches prefixes.
			_, _ = w.Write([]byte(`[{"ID":"s1","Spec":{"Name":"app"}},{"ID":"s2","Spec":{"Name":"app-other"}}]`))
		case "/services/s1/logs":
			query = r.URL.RawQuery
			_, _ = w.Write(append(dockerFrame(1, "2026-09-29T07:00:00Z listening\n"), dockerFrame(2, "2026-09-29T07:00:01Z warn\n")...))
		default:
			t.Errorf("unexpected request %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	client := &swarmProxyClient{baseURL: server.URL, http: server.Client()}

	logs, err := client.Logs(t.Context(), "app", "", 50)
	require.NoError(t, err)
	assert.Equal(t, "2026-09-29T07:00:00Z listening\n2026-09-29T07:00:01Z warn", logs)
	assert.Contains(t, query, "tail=50")
	assert.Contains(t, query, "timestamps=1")
}
