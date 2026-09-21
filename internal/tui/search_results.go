package tui

import (
	"fmt"
	"strings"
)

const searchCategoryLimit = 10

func (m *Model) setSearchGroupExpanded(kind resultKind, expanded bool) {
	if m.expandedSearchGroups == nil {
		m.expandedSearchGroups = make(map[resultKind]bool)
	}
	m.expandedSearchGroups[kind] = expanded
}

// Limit already-ranked categories without changing their order. A disclosure
// stays above its extra results, so expansion does not move the cursor past
// the newly revealed entries.
func (m *Model) searchSwitcherResults(all []searchResult, selected searchResult) []searchResult {
	if selected.WorkspaceParentID != "" {
		// A nested agent needs its workspace kept visible, not its duplicate
		// in the top-level agent category.
		selected = searchResult{Kind: resultWorkspace, ID: selected.WorkspaceParentID}
	}
	out := make([]searchResult, 0, min(len(all), searchCategoryLimit+1))
	for start := 0; start < len(all); {
		kind := all[start].Kind
		end := start + 1
		for end < len(all) && all[end].Kind == kind {
			end++
		}
		group := all[start:end]
		visible := min(searchCategoryLimit, len(group))
		// An unchanged query must keep its selected resource visible even if
		// background activity moves it below the collapsed category limit.
		if selected.ID != "" && selected.Kind == kind && !m.expandedSearchGroups[kind] {
			for _, result := range group[visible:] {
				if result.ID == selected.ID {
					m.setSearchGroupExpanded(kind, true)
					break
				}
			}
		}
		out = append(out, group[:visible]...)
		if remaining := len(group) - visible; remaining > 0 {
			action := "expand"
			if m.expandedSearchGroups[kind] {
				action = "collapse"
			}
			out = append(out, searchResult{
				Kind: resultDisclosure, Title: "More " + strings.ToLower(groupTitle(kind)),
				Detail:     fmt.Sprintf("%d more · tab to %s", remaining, action),
				Disclosure: "search-" + string(kind), DisclosureGroup: kind, DisclosureCount: remaining,
			})
			if m.expandedSearchGroups[kind] {
				out = append(out, group[visible:]...)
			}
		}
		start = end
	}
	return out
}
