package piagent

import "testing"

func TestCoordinationMessageRendering(t *testing.T) {
	runReviewFixture(t, "coordination-renderer-test.ts", "GALPON_COORDINATION_RENDER_TEST_RESULT")
}
