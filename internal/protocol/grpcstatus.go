package protocol

import (
	"io"
	"strings"

	http "github.com/bogdanfinn/fhttp"
)

// readGRPCStatus returns grpc-status from headers or grpc-web body trailers.
func readGRPCStatus(resp *http.Response, body []byte) string {
	if resp != nil {
		if st := strings.TrimSpace(resp.Header.Get("grpc-status")); st != "" {
			return st
		}
		if st := strings.TrimSpace(resp.Trailer.Get("grpc-status")); st != "" {
			return st
		}
	}
	return parseGRPCStatusFromBody(body)
}

func parseGRPCStatusFromBody(body []byte) string {
	if len(body) == 0 {
		return ""
	}
	s := string(body)
	if i := strings.LastIndex(s, "grpc-status:"); i >= 0 {
		rest := s[i+len("grpc-status:"):]
		rest = strings.TrimLeft(rest, " \t")
		end := len(rest)
		for j := 0; j < len(rest); j++ {
			if rest[j] == '\r' || rest[j] == '\n' || rest[j] == ' ' {
				end = j
				break
			}
		}
		return strings.TrimSpace(rest[:end])
	}
	return ""
}

func readAllBody(resp *http.Response) ([]byte, error) {
	if resp == nil || resp.Body == nil {
		return nil, nil
	}
	defer resp.Body.Close()
	return io.ReadAll(io.LimitReader(resp.Body, 4<<20))
}
