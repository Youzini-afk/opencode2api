package identity

import (
	"bytes"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"regexp"
)

const defaultOpenCodeVersion = "1.18.18"

var defaultOpenCodeVersions = []string{"1.18.18", "1.18.17", "1.18.16", "1.18.15", "1.18.14", "1.18.13", "1.18.12", "1.18.11", "1.18.10", "1.18.9", "1.18.8", "1.18.7"}
var gitProjectIDPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)

func normalizeProjectID(explicit, session string) string {
	if explicit != "" {
		if explicit == "global" || gitProjectIDPattern.MatchString(explicit) {
			return explicit
		}
		return sha1Hex(explicit)
	}
	return sha1Hex(session)
}

func sha1Hex(value string) string {
	sum := sha1.Sum([]byte(value))
	return hex.EncodeToString(sum[:])
}

func internalKeySignal(value string) string {
	if value == "" {
		return "anonymous"
	}
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func userAgentForSession(session string, versions []string) string {
	return "opencode/" + rendezvousVersion(session, versions)
}

func defaultOpenCodeUserAgent() string {
	return "opencode/" + defaultOpenCodeVersion
}

// rendezvousVersion uses highest-random-weight hashing so adding or removing a
// version remaps only sessions whose winning version changed.
func rendezvousVersion(session string, versions []string) string {
	if len(versions) == 0 {
		return defaultOpenCodeVersion
	}
	bestVersion := ""
	var bestScore [sha256.Size]byte
	for _, candidate := range versions {
		score := sha256.Sum256([]byte(session + "\x00" + candidate))
		if bestVersion == "" || bytes.Compare(score[:], bestScore[:]) > 0 || score == bestScore && candidate > bestVersion {
			bestVersion, bestScore = candidate, score
		}
	}
	return bestVersion
}

func SetHeaders(req *http.Request, ids RequestIDs) {
	if ids.UserAgent == "" {
		ids.UserAgent = defaultOpenCodeUserAgent()
	}
	req.Header.Set("User-Agent", ids.UserAgent)
	req.Header.Set("x-opencode-client", "cli")
	req.Header.Set("x-opencode-session", ids.Session)
	req.Header.Set("x-opencode-request", ids.Request)
	req.Header.Set("x-opencode-project", ids.Project)
	req.Header.Set("x-session-affinity", ids.Session)
	req.Header.Set("X-Session-Id", ids.Session)
	if ids.ParentSession != "" {
		req.Header.Set("x-parent-session-id", ids.ParentSession)
	}
}

func versionPool(values [][]string) []string {
	if len(values) > 0 && len(values[0]) > 0 {
		return values[0]
	}
	return defaultOpenCodeVersions
}
func Internal(signal, requestID string, versions []string) RequestIDs {
	if requestID == "" {
		requestID = RandomID("req", 16)
	}
	legacy := StableID("ses", "internal\x00"+signal)
	return RequestIDs{Session: CanonicalSessionID("internal\x00" + signal), Request: requestID, Project: normalizeProjectID("", legacy), UserAgent: userAgentForSession(legacy, versions)}
}
