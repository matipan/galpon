package main

import (
	"context"
	"fmt"
)

// loadRun makes the containers of each load commit different from those of
// the other load commits, so that no engine gives a cached result.
const loadRun = "c"

// hold runs a container for 12 minutes that prints 5 lines each second, as a
// long check with logs does. Its session stays open on its session runner
// for that time.
func (m *TestEnv) hold(ctx context.Context, n int) error {
	script := fmt.Sprintf(`i=0
while [ $i -lt 3600 ]; do
  i=$((i+1))
  echo "hold %d line $i at $(date +%%T.%%N): the quick brown fox jumps over the lazy dog"
  [ $((i %% 5)) -eq 0 ] && sleep 1
done
true`, n)
	_, err := dag.Container().
		From("alpine:3.22").
		WithEnvVariable("LOAD_RUN", loadRun).
		WithExec([]string{"sh", "-c", script}).
		Sync(ctx)
	return err
}

// Hold01 keeps one session open for 12 minutes.
// +check
func (m *TestEnv) Hold01(ctx context.Context) error { return m.hold(ctx, 1) }

// Hold02 keeps one session open for 12 minutes.
// +check
func (m *TestEnv) Hold02(ctx context.Context) error { return m.hold(ctx, 2) }

// Hold03 keeps one session open for 12 minutes.
// +check
func (m *TestEnv) Hold03(ctx context.Context) error { return m.hold(ctx, 3) }

// Hold04 keeps one session open for 12 minutes.
// +check
func (m *TestEnv) Hold04(ctx context.Context) error { return m.hold(ctx, 4) }

// Hold05 keeps one session open for 12 minutes.
// +check
func (m *TestEnv) Hold05(ctx context.Context) error { return m.hold(ctx, 5) }

// Hold06 keeps one session open for 12 minutes.
// +check
func (m *TestEnv) Hold06(ctx context.Context) error { return m.hold(ctx, 6) }

// Hold07 keeps one session open for 12 minutes.
// +check
func (m *TestEnv) Hold07(ctx context.Context) error { return m.hold(ctx, 7) }

// Hold08 keeps one session open for 12 minutes.
// +check
func (m *TestEnv) Hold08(ctx context.Context) error { return m.hold(ctx, 8) }

// Hold09 keeps one session open for 12 minutes.
// +check
func (m *TestEnv) Hold09(ctx context.Context) error { return m.hold(ctx, 9) }

// Hold10 keeps one session open for 12 minutes.
// +check
func (m *TestEnv) Hold10(ctx context.Context) error { return m.hold(ctx, 10) }

// Hold11 keeps one session open for 12 minutes.
// +check
func (m *TestEnv) Hold11(ctx context.Context) error { return m.hold(ctx, 11) }

// Hold12 keeps one session open for 12 minutes.
// +check
func (m *TestEnv) Hold12(ctx context.Context) error { return m.hold(ctx, 12) }

// Hold13 keeps one session open for 12 minutes.
// +check
func (m *TestEnv) Hold13(ctx context.Context) error { return m.hold(ctx, 13) }

// Hold14 keeps one session open for 12 minutes.
// +check
func (m *TestEnv) Hold14(ctx context.Context) error { return m.hold(ctx, 14) }

// Hold15 keeps one session open for 12 minutes.
// +check
func (m *TestEnv) Hold15(ctx context.Context) error { return m.hold(ctx, 15) }

// Hold16 keeps one session open for 12 minutes.
// +check
func (m *TestEnv) Hold16(ctx context.Context) error { return m.hold(ctx, 16) }

// Hold17 keeps one session open for 12 minutes.
// +check
func (m *TestEnv) Hold17(ctx context.Context) error { return m.hold(ctx, 17) }

// Hold18 keeps one session open for 12 minutes.
// +check
func (m *TestEnv) Hold18(ctx context.Context) error { return m.hold(ctx, 18) }

// Hold19 keeps one session open for 12 minutes.
// +check
func (m *TestEnv) Hold19(ctx context.Context) error { return m.hold(ctx, 19) }

// Hold20 keeps one session open for 12 minutes.
// +check
func (m *TestEnv) Hold20(ctx context.Context) error { return m.hold(ctx, 20) }

// Hold21 keeps one session open for 12 minutes.
// +check
func (m *TestEnv) Hold21(ctx context.Context) error { return m.hold(ctx, 21) }

// Hold22 keeps one session open for 12 minutes.
// +check
func (m *TestEnv) Hold22(ctx context.Context) error { return m.hold(ctx, 22) }

// Hold23 keeps one session open for 12 minutes.
// +check
func (m *TestEnv) Hold23(ctx context.Context) error { return m.hold(ctx, 23) }

// Hold24 keeps one session open for 12 minutes.
// +check
func (m *TestEnv) Hold24(ctx context.Context) error { return m.hold(ctx, 24) }

// Hold25 keeps one session open for 12 minutes.
// +check
func (m *TestEnv) Hold25(ctx context.Context) error { return m.hold(ctx, 25) }

// Hold26 keeps one session open for 12 minutes.
// +check
func (m *TestEnv) Hold26(ctx context.Context) error { return m.hold(ctx, 26) }

// Hold27 keeps one session open for 12 minutes.
// +check
func (m *TestEnv) Hold27(ctx context.Context) error { return m.hold(ctx, 27) }

// Hold28 keeps one session open for 12 minutes.
// +check
func (m *TestEnv) Hold28(ctx context.Context) error { return m.hold(ctx, 28) }

// Hold29 keeps one session open for 12 minutes.
// +check
func (m *TestEnv) Hold29(ctx context.Context) error { return m.hold(ctx, 29) }

// Hold30 keeps one session open for 12 minutes.
// +check
func (m *TestEnv) Hold30(ctx context.Context) error { return m.hold(ctx, 30) }

// Hold31 keeps one session open for 12 minutes.
// +check
func (m *TestEnv) Hold31(ctx context.Context) error { return m.hold(ctx, 31) }

// Hold32 keeps one session open for 12 minutes.
// +check
func (m *TestEnv) Hold32(ctx context.Context) error { return m.hold(ctx, 32) }

// Hold33 keeps one session open for 12 minutes.
// +check
func (m *TestEnv) Hold33(ctx context.Context) error { return m.hold(ctx, 33) }

// Hold34 keeps one session open for 12 minutes.
// +check
func (m *TestEnv) Hold34(ctx context.Context) error { return m.hold(ctx, 34) }

// Hold35 keeps one session open for 12 minutes.
// +check
func (m *TestEnv) Hold35(ctx context.Context) error { return m.hold(ctx, 35) }

// Hold36 keeps one session open for 12 minutes.
// +check
func (m *TestEnv) Hold36(ctx context.Context) error { return m.hold(ctx, 36) }

// Hold37 keeps one session open for 12 minutes.
// +check
func (m *TestEnv) Hold37(ctx context.Context) error { return m.hold(ctx, 37) }

// Hold38 keeps one session open for 12 minutes.
// +check
func (m *TestEnv) Hold38(ctx context.Context) error { return m.hold(ctx, 38) }

// Hold39 keeps one session open for 12 minutes.
// +check
func (m *TestEnv) Hold39(ctx context.Context) error { return m.hold(ctx, 39) }

// Hold40 keeps one session open for 12 minutes.
// +check
func (m *TestEnv) Hold40(ctx context.Context) error { return m.hold(ctx, 40) }
