package e2e

import "testing"

// skipOnDemo keeps the end-to-end suite out of the runs of the BYOC demo. Its
// tests start real terminals and have time limits that a small machine with
// other work does not always keep.
func skipOnDemo(t *testing.T) {
	t.Helper()
	t.Skip("the end-to-end suite does not run on the branches of the BYOC demo")
}
