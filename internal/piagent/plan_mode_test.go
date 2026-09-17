package piagent

import "testing"

func TestNativePlanController(t *testing.T) {
	runReviewFixture(t, "plan-test.ts", "GALPON_PLAN_TEST_RESULT")
}

func TestNativePlanForegroundLaunch(t *testing.T) {
	runReviewFixture(t, "plan-launch-test.ts", "GALPON_PLAN_LAUNCH_TEST_RESULT")
}
