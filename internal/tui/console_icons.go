package tui

// Keep these meanings aligned with the Pi console symbols in tool-frame.ts.
// Entity, state, and control symbols are separate; tree lines are not icons.
type consoleIcon string

const (
	iconBrand      consoleIcon = "⌂"
	iconSection    consoleIcon = "▰"
	iconAgent      consoleIcon = "◈"
	iconWorkspace  consoleIcon = "▦"
	iconRepository consoleIcon = "▣"
	iconWorktree   consoleIcon = "⑂"
	iconDelegation consoleIcon = "⊶"
	iconMessage    consoleIcon = "✉"
	iconFocus      consoleIcon = "›"
	iconCollapsed  consoleIcon = "▸"
	iconExpanded   consoleIcon = "▾"
	iconAdd        consoleIcon = "+"
	iconMore       consoleIcon = "…"
	iconPending    consoleIcon = "○"
	iconIdle       consoleIcon = "–"
	iconAttention  consoleIcon = "!"
	iconSuccess    consoleIcon = "✓"
	iconFailure    consoleIcon = "×"
	iconCanceled   consoleIcon = "⊘"
	iconStopped    consoleIcon = "■"
	iconUnknown    consoleIcon = "?"
)

var consoleIconASCII = map[consoleIcon]string{
	iconBrand: "[G]", iconSection: "::", iconAgent: "[A]", iconWorkspace: "[W]",
	iconRepository: "[R]", iconWorktree: "[T]", iconDelegation: "&", iconMessage: "[M]",
	iconFocus: ">", iconCollapsed: "[+]", iconExpanded: "[-]", iconAdd: "+", iconMore: "...",
	iconPending: "o", iconIdle: "-", iconAttention: "!", iconSuccess: "v", iconFailure: "x",
	iconCanceled: "/", iconStopped: "#", iconUnknown: "?",
}

func consoleMark(icon consoleIcon) string {
	return consoleGlyph(string(icon), consoleIconASCII[icon])
}
