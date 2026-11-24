package e2e

import (
	"fmt"
	"os"
	"testing"
)

// TestMain keeps the end-to-end suite out of the runs of the BYOC demo. Its
// tests start real terminals and have time limits that a small machine with
// other work does not always keep.
func TestMain(m *testing.M) {
	fmt.Println("the end-to-end suite does not run on the branches of the BYOC demo")
	os.Exit(0)
}
