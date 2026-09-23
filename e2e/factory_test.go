package e2e

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	factorysvc "github.com/matipan/galpon/internal/factory"
	"github.com/matipan/galpon/internal/model"
)

func TestFactoryPlannerWorkflow(t *testing.T) {
	piBin, err := exec.LookPath("pi")
	if err != nil {
		t.Skip("Pi is not installed")
	}
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() { _ = r.Body.Close() }()
		var request map[string]any
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeTextResponse(w, "Factory plan result")
	}))
	defer mock.Close()
	root := t.TempDir()
	stateDir := filepath.Join(root, "state")
	piHome := filepath.Join(root, "pi")
	writePiConfig(t, piHome, mock.URL)
	env := append(os.Environ(), "GALPON_STATE_DIR="+stateDir, "GALPON_PI_BIN="+piBin, "GALPON_PI_PROVIDER=galpon-mock", "GALPON_PI_MODEL=mock-model", "GALPON_HERDR_BIN=herdr-not-used", "PI_CODING_AGENT_DIR="+piHome, "PI_OFFLINE=1", "GALPON_TEST_SKIP_PI_PACKAGE_SETUP=1")
	bin := filepath.Join(root, "galpon")
	runRaw(t, "..", nil, "go", "build", "-o", bin, "./cmd/galpon")
	defer func() {
		_ = runCommand("", env, bin, "factory", "stop")
		_ = runCommand("", env, bin, "daemon", "stop")
	}()
	repositoryPath := createRepository(t, root)
	var repository model.Repository
	decodeCommand(t, &repository, runRaw(t, "", env, bin, "repo", "add", repositoryPath))
	var workspace model.Workspace
	decodeCommand(t, &workspace, runRaw(t, "", env, bin, "workspace", "create", "Factory E2E"))
	if output := runRaw(t, "", env, bin, "factory", "start"); output != "Factory is running\n" {
		t.Fatalf("factory start = %q", output)
	}
	client := factorysvc.NewClient(filepath.Join(stateDir, "factory", "factory.sock"))
	order, err := client.Create(t.Context(), factorysvc.CreateRequest{Title: "Factory feature", Request: "Prepare a safe implementation plan", RepositoryID: repository.ID, RepositoryPath: repository.SourcePath, WorkspaceID: workspace.ID})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		snapshot, err := client.Get(t.Context(), order.ID)
		if err == nil && len(snapshot.WorkOrders) == 1 && snapshot.WorkOrders[0].Stage == factorysvc.StagePlanApproval {
			if snapshot.WorkOrders[0].Plan != "Factory plan result" {
				t.Fatalf("plan = %q", snapshot.WorkOrders[0].Plan)
			}
			if _, err := os.Stat(filepath.Join(stateDir, "factory", "factory.db")); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(filepath.Join(stateDir, "galpon.db")); err != nil {
				t.Fatal(err)
			}
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	snapshot, _ := client.Get(t.Context(), order.ID)
	t.Fatalf("Factory did not reach plan approval: %#v", snapshot)
}
