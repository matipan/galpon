package screen

import (
	"os"
	"os/exec"
	"runtime"
	"strings"
	"testing"
)

func TestBuildIDIsTheIDThatTheGoToolchainGives(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("BuildID reads ELF files")
	}
	goTool, err := exec.LookPath("go")
	if err != nil {
		t.Skip("no go command")
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(goTool, "tool", "buildid", executable).Output()
	if err != nil {
		t.Fatalf("go tool buildid: %v", err)
	}
	got, err := BuildID(executable)
	if err != nil {
		t.Fatalf("BuildID: %v", err)
	}
	if want := strings.TrimSpace(string(out)); got != want {
		t.Fatalf("BuildID = %q, want %q", got, want)
	}
}
