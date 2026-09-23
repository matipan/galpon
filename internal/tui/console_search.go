package tui

import "sort"

// Keep the initial relevance order until the query changes or the user asks
// to regroup. Runtime timestamp updates must not move rows under the cursor.
func (m *Model) holdSearchOrder(rows []searchResult) {
	if m.controlOrder == nil {
		m.controlOrder = map[string]int{}
	}
	for _, row := range rows {
		key := controlKey(row)
		if _, ok := m.controlOrder[key]; !ok {
			m.controlOrder[key] = len(m.controlOrder)
		}
	}
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].Kind != rows[j].Kind {
			return resultOrder(rows[i]) < resultOrder(rows[j])
		}
		return m.controlOrder[controlKey(rows[i])] < m.controlOrder[controlKey(rows[j])]
	})
}
