package tools

import "testing"

// Which groups survive the limit is the same on every call: two groups of
// one series and one title in different seasons used to swap places with
// the order the sweep's map happened to give them, so a capped worklist
// named a different group each time it was asked.
func TestAuditDuplicateEpisodesKeepsItsOrderAcrossCalls(t *testing.T) {
	t.Parallel()

	s := &fakeSeries{id: "z", name: "Zzyzx Show", episodes: []ep{
		{season: 1, number: 1, name: "Pilot", path: "/m/s1e1.mkv", minutes: 45},
		{season: 1, number: 2, name: "Pilot", path: "/m/s1e2.mkv", minutes: 45},
		{season: 2, number: 1, name: "Pilot", path: "/m/s2e1.mkv", minutes: 45},
		{season: 2, number: 2, name: "Pilot", path: "/m/s2e2.mkv", minutes: 45},
	}}
	f := tvServer(t, s)
	adminView(t, f)
	cs := session(t, f, Options{})

	for range 20 {
		out := mustCall(t, cs, "audit_duplicate_episodes", map[string]any{"limit": 1})
		groups := objects(t, out["groups"], "groups")
		if len(groups) != 1 || number(t, out["total_findings"], "total_findings") != 2 {
			t.Fatalf("out = %v", out)
		}
		if season := number(t, groups[0]["season"], "season"); season != 1 {
			t.Fatalf("the group kept under the limit is season %d, want season 1 every time", season)
		}
	}
}
