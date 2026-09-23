package factory

import "testing"

func TestParsePRChecks(t *testing.T) {
	tests := []struct {
		name, json, want string
		merged           bool
	}{{"none", `{"number":1,"url":"u","state":"OPEN","statusCheckRollup":[]}`, "none", false}, {"pending", `{"number":2,"url":"u","state":"OPEN","statusCheckRollup":[{"status":"IN_PROGRESS","conclusion":""}]}`, "pending", false}, {"failure", `{"number":3,"url":"u","state":"OPEN","statusCheckRollup":[{"status":"COMPLETED","conclusion":"FAILURE"}]}`, "failure", false}, {"merged", `{"number":4,"url":"u","state":"MERGED","mergedAt":"now","statusCheckRollup":[{"status":"COMPLETED","conclusion":"SUCCESS"}]}`, "success", true}}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := parsePR([]byte(test.json))
			if err != nil {
				t.Fatal(err)
			}
			if got.Checks != test.want || got.Merged != test.merged {
				t.Fatalf("got %#v", got)
			}
		})
	}
}
