package main

import (
	"bytes"
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"
)

const defaultOpenCodeVersion = "1.18.18"

var defaultOpenCodeVersions = []string{
	"1.18.18", "1.18.17", "1.18.16", "1.18.15", "1.18.14", "1.18.13",
	"1.18.12", "1.18.11", "1.18.10", "1.18.9", "1.18.8", "1.18.7",
}

var gitProjectIDPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)

type requestIDs struct {
	Session   string
	Request   string
	Project   string
	UserAgent string
}

func deriveRequestIDs(r *http.Request, body map[string]any, versions []string) requestIDs {
	signal := firstString(
		r.Header.Get("x-opencode-session"),
		r.Header.Get("x-session-id"),
		r.Header.Get("conversation-id"),
		stringAt(body, "conversation_id"),
		stringAt(body, "metadata", "session_id"),
	)
	if signal == "" {
		// Using the first user turn keeps a multi-turn conversation stable as its
		// history grows while separating conversations with different beginnings.
		signal = conversationSeed(body)
	}
	if signal == "" {
		signal = stringAt(body, "previous_response_id")
	}
	if signal == "" || signal == `{}` {
		signal = randomID("fallback", 16)
	}
	session := stableID("ses", signal)
	projectSignal := firstString(r.Header.Get("x-opencode-project"), stringAt(body, "metadata", "project_id"))
	return requestIDs{
		Session:   session,
		Request:   randomID("req", 16),
		Project:   normalizeProjectID(projectSignal, session),
		UserAgent: userAgentForSession(session, versions),
	}
}

func deriveInternalRequestIDs(signal, requestID string, versions []string) requestIDs {
	session := stableID("ses", "internal\x00"+signal)
	if requestID == "" {
		requestID = randomID("req", 16)
	}
	return requestIDs{
		Session:   session,
		Request:   requestID,
		Project:   normalizeProjectID("", session),
		UserAgent: userAgentForSession(session, versions),
	}
}

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

func conversationSeed(body map[string]any) string {
	if input, ok := body["input"].(string); ok && input != "" {
		return input
	}
	for _, field := range []string{"messages", "input"} {
		for _, raw := range sliceAt(body, field) {
			item, ok := raw.(map[string]any)
			if !ok || stringAt(item, "role") != "user" {
				continue
			}
			encoded, _ := json.Marshal(item["content"])
			if len(encoded) > 0 && string(encoded) != "null" {
				return string(encoded)
			}
		}
	}
	return ""
}

func stableID(prefix, value string) string {
	sum := sha256.Sum256([]byte(prefix + "\x00" + value))
	return prefix + "_" + hex.EncodeToString(sum[:12])
}

func randomID(prefix string, size int) string {
	buf := make([]byte, size)
	if _, err := rand.Read(buf); err != nil {
		panic(fmt.Sprintf("crypto/rand failed: %v", err))
	}
	return prefix + "_" + hex.EncodeToString(buf)
}

func firstString(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
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

func setOpenCodeIdentityHeaders(req *http.Request, ids requestIDs) {
	req.Header.Set("User-Agent", ids.UserAgent)
	req.Header.Set("x-opencode-client", "cli")
	req.Header.Set("x-opencode-session", ids.Session)
	req.Header.Set("x-opencode-request", ids.Request)
	req.Header.Set("x-opencode-project", ids.Project)
}
