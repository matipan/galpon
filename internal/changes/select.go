package changes

import "strings"

// Pick is the change set that the view shows: a checkout, a branch of it
// and a set of that branch. Follow tells if the view moves to the branch
// that the checkout shows when the agent switches branches.
type Pick struct {
	Selection
	Follow bool `json:"follow,omitempty"`
}

// Choose resolves the change set that the view asks for against the
// current work. A pick of a checkout and a branch that the work has stays.
// A pick that follows its checkout moves to the branch that the checkout
// shows now, and Choose then reports that it followed. Any other pick, and
// no pick, gives the default of the work, which follows its checkout. The
// result is nil when the work has no branch to show.
func Choose(work Work, ask *Pick) (*Pick, bool) {
	if ask != nil {
		if checkout, branch := findWorkBranch(work, ask.Checkout, ask.Branch); branch != nil {
			if ask.Follow {
				for i := range checkout.Branches {
					live := &checkout.Branches[i]
					if live.Live && live.Key != branch.Key {
						return &Pick{Selection: Selection{Checkout: checkout.Key, Branch: live.Key, Set: SetFor(*live)}, Follow: true}, true
					}
				}
			}
			pick := *ask
			if isBranchTotal(pick.Set) {
				pick.Set = SetFor(*branch)
			}
			return &pick, false
		}
	}
	if work.Default == nil {
		return nil, false
	}
	return &Pick{Selection: *work.Default, Follow: true}, false
}

// SetFor gives the change set of the total of a branch.
func SetFor(branch Branch) string {
	if branch.Detached {
		return "detached"
	}
	return "branch:" + branch.Name
}

// BaseFor gives the commit that the total of a branch starts from, or ""
// for the other change sets, which name their own commits.
func BaseFor(work Work, pick Pick) string {
	if !isBranchTotal(pick.Set) {
		return ""
	}
	if _, branch := findWorkBranch(work, pick.Checkout, pick.Branch); branch != nil {
		return branch.Base.MergeBase
	}
	return ""
}

func isBranchTotal(set string) bool {
	return set == "detached" || strings.HasPrefix(set, "branch:")
}

func findWorkBranch(work Work, checkoutKey, branchKey string) (*Checkout, *Branch) {
	for r := range work.Repositories {
		for c := range work.Repositories[r].Checkouts {
			checkout := &work.Repositories[r].Checkouts[c]
			if checkout.Key != checkoutKey {
				continue
			}
			for b := range checkout.Branches {
				if checkout.Branches[b].Key == branchKey {
					return checkout, &checkout.Branches[b]
				}
			}
			return checkout, nil
		}
	}
	return nil, nil
}
