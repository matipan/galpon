package tui

import (
	"regexp"
	"strings"
)

// deliveryHeader is the first line of a Galpon delivery as a Pi agent gets
// it, such as "Work request from Galpón agent Planner [delivery 12]:".
var deliveryHeader = regexp.MustCompile(`^(?:Work request|Question|One-way information|Message \d+ of \d+|Blocked work notification)(?: from Galpón agent (.+?))? \[delivery [^\]]+\]:\n\n`)

// DeliveryText gives the text of a message to an agent without the protocol
// lines of a Galpon delivery, and the agent that sent it.
func DeliveryText(text string) (string, string) {
	match := deliveryHeader.FindStringSubmatchIndex(text)
	if match == nil {
		return text, ""
	}
	from := ""
	if match[2] >= 0 {
		from = text[match[2]:match[3]]
	}
	body := text[match[1]:]
	if index := strings.Index(body, "\n\n---\n\nDelivery instructions:"); index >= 0 {
		body = body[:index]
	}
	return strings.TrimSpace(body), from
}

var (
	excerptHeading = regexp.MustCompile(`^#{1,6}\s+`)
	excerptBold    = regexp.MustCompile(`\*\*(.+?)\*\*`)
	excerptCode    = regexp.MustCompile("`([^`]+)`")
	excerptBullet  = regexp.MustCompile(`^\s*[-*]\s+`)
	excerptRule    = regexp.MustCompile(`^\|?\s*-{3,}.*$`)
)

// excerpt makes the first lines of a reply readable in a few rows: no
// Markdown marks, no code fences, no blank runs, and at most limit lines.
func excerpt(text string, limit int) []string {
	var out []string
	fence := false
	for _, raw := range strings.Split(text, "\n") {
		if strings.HasPrefix(strings.TrimSpace(raw), "```") {
			fence = !fence
			continue
		}
		line := strings.TrimRight(raw, " \t\r")
		if !fence {
			line = excerptHeading.ReplaceAllString(line, "")
			line = excerptBold.ReplaceAllString(line, "$1")
			line = excerptCode.ReplaceAllString(line, "$1")
			line = excerptBullet.ReplaceAllString(line, "• ")
			line = excerptRule.ReplaceAllString(line, "")
		}
		if strings.TrimSpace(line) == "" && (len(out) == 0 || strings.TrimSpace(out[len(out)-1]) == "") {
			continue
		}
		out = append(out, line)
		if len(out) >= limit {
			break
		}
	}
	for len(out) > 0 && strings.TrimSpace(out[len(out)-1]) == "" {
		out = out[:len(out)-1]
	}
	return out
}
