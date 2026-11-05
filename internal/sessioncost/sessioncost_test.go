package sessioncost

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func reply(role string, cost float64, at time.Time) string {
	return fmt.Sprintf(`{"type":"message","timestamp":%q,"message":{"role":%q,"content":[{"type":"text","text":"the cost is high"}],"usage":{"input":10,"cost":{"input":0,"total":%v}}}}`+"\n", at.UTC().Format(time.RFC3339Nano), role, cost)
}

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func TestReadSumsTheCostsThatThePiFooterSums(t *testing.T) {
	now := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	old := now.Add(-time.Hour)
	path := filepath.Join(t.TempDir(), "session.jsonl")
	lines := `{"type":"session","version":3,"id":"s1","timestamp":"2026-11-05T10:00:00Z","cwd":"/src"}` + "\n" +
		`{"type":"message","timestamp":"2026-11-05T10:00:01Z","message":{"role":"user","content":[{"type":"text","text":"what does \"cost\" mean?"}]}}` + "\n" +
		reply("assistant", 1.5, old) +
		reply("toolResult", 0.25, old) +
		`{"type":"compaction","timestamp":"2026-11-05T11:00:00Z","summary":"...","usage":{"cost":{"total":0.5}}}` + "\n" +
		`{"type":"usage","timestamp":"2026-11-05T11:00:00Z","usage":{"cost":{"total":0.125}}}` + "\n" +
		`{"type":"branch_summary","timestamp":"2026-11-05T11:00:00Z","usage":{"cost":{"total":0.0625}}}` + "\n" +
		`{"type":"custom","timestamp":"2026-11-05T11:00:00Z","usage":{"cost":{"total":100}}}` + "\n" +
		reply("assistant", 2, now.Add(-time.Minute))
	if err := os.WriteFile(path, []byte(lines), 0o600); err != nil {
		t.Fatal(err)
	}
	cost, err := Open(filepath.Join(t.TempDir(), "cache.json")).Read(path, now)
	if err != nil {
		t.Fatal(err)
	}
	if !near(cost.Total, 4.4375) || !near(cost.Recent, 2) {
		t.Fatalf("cost = %+v, want total 4.4375 and recent 2", cost)
	}
}

func TestReadContinuesFromTheCacheAndWaitsForTheEndOfALine(t *testing.T) {
	now := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	dir := t.TempDir()
	path := filepath.Join(dir, "session.jsonl")
	cachePath := filepath.Join(dir, "cache.json")
	first := reply("assistant", 1, now.Add(-time.Hour))
	next := reply("assistant", 2, now.Add(-2*time.Minute))
	if err := os.WriteFile(path, []byte(first+next[:20]), 0o600); err != nil {
		t.Fatal(err)
	}
	cache := Open(cachePath)
	if cost, err := cache.Read(path, now); err != nil || !near(cost.Total, 1) {
		t.Fatalf("first read = %+v, %v; want total 1", cost, err)
	}
	if err := cache.Save(now); err != nil {
		t.Fatal(err)
	}

	// Pi ends the line, and a new process reads with the saved cache.
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(next[20:]); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	cost, err := Open(cachePath).Read(path, now)
	if err != nil || !near(cost.Total, 3) || !near(cost.Recent, 2) {
		t.Fatalf("second read = %+v, %v; want total 3 and recent 2", cost, err)
	}
	// Ten minutes later, the reply is no longer recent.
	cost, err = Open(cachePath).Read(path, now.Add(Window))
	if err != nil || !near(cost.Total, 3) || cost.Recent != 0 {
		t.Fatalf("later read = %+v, %v; want total 3 and no recent cost", cost, err)
	}
}

func TestReadStartsAgainWhenTheFileChangesBeforeTheReadPosition(t *testing.T) {
	now := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	dir := t.TempDir()
	path := filepath.Join(dir, "session.jsonl")
	cache := Open(filepath.Join(dir, "cache.json"))
	if err := os.WriteFile(path, []byte(reply("assistant", 4, now)+reply("assistant", 4, now)), 0o600); err != nil {
		t.Fatal(err)
	}
	if cost, err := cache.Read(path, now); err != nil || !near(cost.Total, 8) {
		t.Fatalf("first read = %+v, %v; want total 8", cost, err)
	}
	// Something writes the file again with other replies and more bytes.
	if err := os.WriteFile(path, []byte(reply("assistant", 0.5, now)+reply("assistant", 0.5, now)+reply("assistant", 0.5, now)), 0o600); err != nil {
		t.Fatal(err)
	}
	if cost, err := cache.Read(path, now); err != nil || !near(cost.Total, 1.5) {
		t.Fatalf("read after the rewrite = %+v, %v; want total 1.5", cost, err)
	}
	// A shorter file also starts again.
	if err := os.WriteFile(path, []byte(reply("assistant", 0.75, now)), 0o600); err != nil {
		t.Fatal(err)
	}
	if cost, err := cache.Read(path, now); err != nil || !near(cost.Total, 0.75) {
		t.Fatalf("read after the file got shorter = %+v, %v; want total 0.75", cost, err)
	}
}
