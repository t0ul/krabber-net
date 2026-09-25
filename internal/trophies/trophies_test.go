package trophies

import (
	"slices"
	"testing"
	"time"
)

func TestCatalog(t *testing.T) {
	seen := map[string]bool{}
	for _, tr := range All {
		if tr.ID == "" || tr.Title == "" || tr.Description == "" || seen[tr.ID] {
			t.Errorf("bad or duplicate entry %+v", tr)
		}
		seen[tr.ID] = true
	}
	for _, m := range [][]milestone{followerMilestones, moltMilestones, likeMilestones} {
		for _, ms := range m {
			if _, ok := Get(ms.id); !ok {
				t.Errorf("milestone %s isn't in the catalog", ms.id)
			}
		}
	}
	for _, id := range hiddenTags {
		if _, ok := Get(id); !ok {
			t.Errorf("hidden tag trophy %s isn't in the catalog", id)
		}
	}
	for _, tr := range Manual() {
		if !tr.Manual {
			t.Errorf("%s listed as manual", tr.ID)
		}
	}
}

func TestRules(t *testing.T) {
	if got := ForFollowers(0, 1, 0); !slices.Equal(got, []string{"social-newbie"}) {
		t.Errorf("first follower: %v", got)
	}
	if got := ForFollowers(1, 2, 0); len(got) != 0 {
		t.Errorf("second follower: %v", got)
	}
	if got := ForFollowers(399, 400, 20); !slices.Equal(got, []string{"twenty-twenty"}) {
		t.Errorf("ratio 20: %v", got)
	}
	if got := ForFollowers(999, 1000, 20); !slices.Contains(got, "celebrity") || slices.Contains(got, "twenty-twenty") {
		t.Errorf("1,000 followers at an old ratio: %v", got)
	}
	if got := ForMolts(0, 1, []string{"lolcat", "420", "lolcat"}); !slices.Equal(got, []string{"baby-krab", "cheezburger", "pineapple-express"}) {
		t.Errorf("first molt with hidden tags: %v", got)
	}
	if got := ForLikes(9, 10); !slices.Equal(got, []string{"dopamine-hit"}) {
		t.Errorf("10th like: %v", got)
	}
	if got := ForLikes(10, 11); len(got) != 0 {
		t.Errorf("11th like: %v", got)
	}
	if !Rogen("I love Seth Rogen", nil) || !Rogen("x", []string{"sethrogen"}) || Rogen("rogan", nil) {
		t.Error("rogen")
	}
	if !CustomizedProfile("Gary", "Meow", "", "https://gary.test", true) || CustomizedProfile("Gary", "Meow", "", "", true) {
		t.Error("customized profile")
	}
	joined := time.Date(2025, 3, 1, 12, 0, 0, 0, time.UTC)
	for when, want := range map[time.Time]bool{
		joined.AddDate(1, 0, 0):                 true,
		joined.AddDate(1, 0, 6):                 true,
		joined.AddDate(1, 0, 8):                 false,
		joined.AddDate(1, 0, -1):                false,
		joined.AddDate(2, 0, 0):                 false,
		joined.AddDate(1, 0, 0).Add(-time.Hour): false,
	} {
		if got := Anniversary(joined, when); got != want {
			t.Errorf("Anniversary(%v) = %v", when, got)
		}
	}
}
