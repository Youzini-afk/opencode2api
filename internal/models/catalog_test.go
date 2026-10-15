package models

import (
	"testing"
	"time"

	"opencode2api/internal/config"
)

func TestFreeOnlyIsExplicitAndProbeFailureDoesNotGateRouting(t *testing.T) {
	c := NewCatalog(config.TierZen, nil)
	c.Replace([]string{"paid-model", "example-free"}, []string{"paid-model", "example-free"})
	s, err := NewAvailabilityStore("")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Record("example-free", 0, false, "model_unavailable", "anonymous", time.Now(), EffortProbe{}); err != nil {
		t.Fatal(err)
	}
	c.SetAvailabilityStore(s)
	for _, filtered := range []bool{false, true, false} {
		c.SetFreeOnly(filtered)
		routes, _ := c.AvailableModels(true, true, true)
		want := 2
		if filtered {
			want = 1
		}
		if len(routes) != want {
			t.Fatalf("free_only=%v: directory=%v, want %d models", filtered, routes, want)
		}
		if _, err := c.Route("example-free", false, false, true); err != nil {
			t.Fatalf("diagnostic failure disabled production inference: %v", err)
		}
		_, normalErr := c.Route("paid-model", true, true, true)
		_, pinnedErr := c.RouteForTier("paid-model", config.TierGo, true, true)
		if (normalErr != nil) != filtered || (pinnedErr != nil) != filtered {
			t.Fatalf("free_only=%v: normal=%v pinned=%v", filtered, normalErr, pinnedErr)
		}
	}
}
