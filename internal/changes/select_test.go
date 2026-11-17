package changes

import "testing"

func TestChooseKeepsFollowsOrFallsBackToTheDefault(t *testing.T) {
	work := Work{
		Repositories: []Repository{{ID: "r", Checkouts: []Checkout{{
			Key: "c",
			Branches: []Branch{
				{Key: "fix", Name: "fix", Live: true, Base: Base{MergeBase: "base-of-fix"}},
				{Key: "old", Name: "old", Base: Base{MergeBase: "base-of-old"}},
			},
		}}}},
		Default: &Selection{Checkout: "c", Branch: "fix", Set: "branch:fix"},
	}
	pick := func(branch, set string, follow bool) *Pick {
		return &Pick{Selection: Selection{Checkout: "c", Branch: branch, Set: set}, Follow: follow}
	}
	for _, test := range []struct {
		name     string
		ask      *Pick
		want     Pick
		followed bool
	}{
		{"no pick gives the default, which follows", nil, *pick("fix", "branch:fix", true), false},
		{"a branch that is gone gives the default", pick("gone", "branch:gone", false), *pick("fix", "branch:fix", true), false},
		{"a pick that does not follow stays", pick("old", "branch:old", false), *pick("old", "branch:old", false), false},
		{"a commit of a branch stays", pick("old", "commit:abc", false), *pick("old", "commit:abc", false), false},
		{"a pick that follows moves to the live branch", pick("old", "branch:old", true), *pick("fix", "branch:fix", true), true},
		{"a pick of the live branch that follows stays", pick("fix", "uncommitted", true), *pick("fix", "uncommitted", true), false},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, followed := Choose(work, test.ask)
			if got == nil || *got != test.want || followed != test.followed {
				t.Fatalf("Choose = %#v, %v; want %#v, %v", got, followed, test.want, test.followed)
			}
		})
	}
	if got, _ := Choose(Work{}, nil); got != nil {
		t.Fatalf("Choose of work without branches = %#v, want nil", got)
	}
	if base := BaseFor(work, *pick("old", "branch:old", false)); base != "base-of-old" {
		t.Fatalf("BaseFor the total of old = %q, want its merge base", base)
	}
	if base := BaseFor(work, *pick("old", "commit:abc", false)); base != "" {
		t.Fatalf("BaseFor a commit = %q, want none", base)
	}
}
