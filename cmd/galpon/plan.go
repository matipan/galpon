package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"syscall"

	"github.com/matipan/galpon/internal/app"
	"github.com/matipan/galpon/internal/config"
	"github.com/matipan/galpon/internal/herdr"
	"github.com/matipan/galpon/internal/tui"
)

func readPlanHandoff(path string) (app.PlanHandoff, error) {
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return app.PlanHandoff{}, err
	}
	defer func() { _ = file.Close() }()
	const limit = app.MaxPlanBytes*6 + 4096
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > limit {
		return app.PlanHandoff{}, fmt.Errorf("plan handoff is not a bounded regular file")
	}
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil || len(data) > limit {
		return app.PlanHandoff{}, fmt.Errorf("plan handoff changed or could not be read")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var request app.PlanHandoff
	if err := decoder.Decode(&request); err != nil {
		return request, err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return request, fmt.Errorf("plan handoff contains trailing data")
	}
	return request, request.Validate()
}

// This command is the terminal adapter for Pi's /plan delegate. Descriptor 3
// returns the result without mixing protocol data with terminal rendering.
func planCommand(cfg config.Config, args []string) error {
	if len(args) != 3 || args[0] != "delegate" || args[1] != "--request" {
		return fmt.Errorf("use /plan delegate inside a foreground Galpon agent")
	}
	request, err := readPlanHandoff(args[2])
	if err != nil {
		return err
	}
	var outputInfo syscall.Stat_t
	if err := syscall.Fstat(3, &outputInfo); err != nil || (outputInfo.Mode&syscall.S_IFMT != syscall.S_IFIFO && outputInfo.Mode&syscall.S_IFMT != syscall.S_IFSOCK) {
		return fmt.Errorf("plan delegation requires the foreground Pi launcher")
	}
	output := os.NewFile(3, "plan-result")
	defer func() { _ = output.Close() }()
	client, err := ensureDaemon(cfg)
	if err != nil {
		return err
	}
	result, err := tui.RunPlanAgentForm(client, herdr.Adapter{Bin: cfg.HerdrBin}, request)
	if err != nil {
		return err
	}
	return json.NewEncoder(output).Encode(result)
}
