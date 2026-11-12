package model

import (
	"encoding/json"
	"os"
	"testing"
)

func TestParsePullRequestReference(t *testing.T) {
	data, err := os.ReadFile("testdata/pull_request_lookup.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct{ Query, Key string }
	if err := json.Unmarshal(data, &cases); err != nil {
		t.Fatal(err)
	}
	for _, test := range cases {
		t.Run(test.Query, func(t *testing.T) {
			ref, ok := ParsePullRequestReference(test.Query)
			key := ""
			if ok {
				key = ref.String()
			}
			if key != test.Key {
				t.Fatalf("reference = %q, want %q", key, test.Key)
			}
		})
	}
}
