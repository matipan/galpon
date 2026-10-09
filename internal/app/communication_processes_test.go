package app

import (
	"encoding/binary"
	"path/filepath"
	"slices"
	"testing"
)

// procArgs2 builds macOS KERN_PROCARGS2 data as the kernel returns it.
func procArgs2(executable string, args, environment, kernel []string) []byte {
	data := binary.LittleEndian.AppendUint32(nil, uint32(len(args)))
	data = append(data, executable...)
	data = append(data, 0, 0, 0, 0) // Path terminator and alignment padding.
	for _, value := range append(append(slices.Clone(args), environment...), "") {
		data = append(append(data, value...), 0)
	}
	for _, value := range kernel {
		data = append(append(data, value...), 0)
	}
	return data
}

func TestRetitledPiProcessRequiresExactTitleAndExecutable(t *testing.T) {
	for _, test := range []struct {
		name       string
		args       []string
		executable string
		want       bool
	}{
		{"npm", []string{"pi", "", ""}, "/usr/local/bin/node", true},
		{"RPC", []string{"pi-rpc"}, "/usr/bin/nodejs", true},
		{"Bun", []string{"pi"}, "/usr/bin/bun", true},
		{"replaced binary", []string{"pi"}, "/opt/pi/pi (deleted)", true},
		{"renamed tool", []string{"pi"}, "/usr/bin/sleep", false},
		{"other Node process", []string{"worker"}, "/usr/bin/node", false},
		{"explicit tool arguments", []string{"pi", "--help"}, "/usr/bin/node", false},
		{"missing arguments", nil, "/usr/bin/node", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := isRetitledPiProcess(test.args, test.executable); got != test.want {
				t.Fatalf("runtime = %t, want %t", got, test.want)
			}
		})
	}
}

func TestMacOSProcessDataIdentifiesOnlyGalponRuntimes(t *testing.T) {
	stateDir, socket := "/Users/me/.local/state/galpon", "/Users/me/.local/state/galpon/galpon.sock"
	environment := []string{"HOME=/Users/me", "GALPON_SOCKET=" + socket, "GALPON_RUNTIME_ID=runtime", "GALPON_AGENT_ID=agent", "PATH=/usr/bin"}
	kernel := []string{"executable_path=/opt/homebrew/bin/pi", "ptr_munge=", "main_stack="}
	runtime := []string{"pi", "--approve", "--session-dir", filepath.Join(stateDir, "agents", "agent", "sessions"), "--extension", filepath.Join(stateDir, "runtime", "pi", "galpon.ts")}

	args, parsedEnvironment, ok := parseProcessArguments(procArgs2("/opt/homebrew/bin/pi", runtime, environment, kernel))
	if !ok || !slices.Equal(args, runtime) || !slices.Equal(parsedEnvironment, environment) {
		t.Fatalf("parsed = %q, %q, %t", args, parsedEnvironment, ok)
	}
	agentID, ok := communicationRuntimeAgent(parsedEnvironment, socket)
	if !ok || agentID != "agent" || !isCommunicationRuntimeCommand(args, stateDir, agentID) {
		t.Fatalf("the Galpon runtime was not identified: agent %q, %t", agentID, ok)
	}

	// A tool started by the runtime inherits its environment, but not Galpon's
	// Pi launch arguments.
	tool, toolEnvironment, ok := parseProcessArguments(procArgs2("/bin/zsh", []string{"zsh", "-c", "go test ./..."}, environment, kernel))
	if !ok {
		t.Fatal("tool process data did not parse")
	}
	if agentID, ok := communicationRuntimeAgent(toolEnvironment, socket); !ok || isCommunicationRuntimeCommand(tool, stateDir, agentID) {
		t.Fatalf("a tool subprocess was identified as a runtime: %q", tool)
	}

	for name, data := range map[string][]byte{
		"empty":           nil,
		"no path end":     binary.LittleEndian.AppendUint32(nil, 1),
		"missing args":    procArgs2("/opt/homebrew/bin/pi", runtime, nil, nil)[:30],
		"negative count":  append(binary.LittleEndian.AppendUint32(nil, uint32(0xffffffff)), "/bin/pi\x00pi\x00"...),
		"count too large": append(binary.LittleEndian.AppendUint32(nil, 1<<20), "/bin/pi\x00pi\x00"...),
	} {
		if args, _, ok := parseProcessArguments(data); ok {
			t.Fatalf("%s data parsed as %q", name, args)
		}
	}
}
