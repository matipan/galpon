package e2e

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/png"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/matipan/galpon/internal/model"
)

func TestCrossHarnessForksPreserveHistoryAndUseTargetTools(t *testing.T) {
	modelEndpoint := &nativeModel{blocked: map[string]chan struct{}{"pi": make(chan struct{}), "claude": make(chan struct{}), "codex": make(chan struct{})}, resume: map[string]chan struct{}{"pi": make(chan struct{}), "claude": make(chan struct{}), "codex": make(chan struct{})}}
	mock := httptest.NewServer(modelEndpoint)
	t.Cleanup(mock.Close)
	instance := newNativeInstance(t, nativeBinaries(t), mock.URL, "", false, useDefaultClaudeHome, func(n *nativeInstance) {
		n.env = slices.DeleteFunc(n.env, func(value string) bool {
			return strings.HasPrefix(value, "GALPON_PI_MODEL=") || strings.HasPrefix(value, "GALPON_PI_PROVIDER=")
		})
		settings, err := json.Marshal(map[string]any{"theme": "galpon-e2e-system", "defaultProvider": "galpon-mock", "defaultModel": "mock-model"})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(n.root, "pi", "settings.json"), settings, 0o600); err != nil {
			t.Fatal(err)
		}
	})
	releaseSources := func() {
		for _, channel := range modelEndpoint.resume {
			select {
			case <-channel:
			default:
				close(channel)
			}
		}
	}
	t.Cleanup(releaseSources)
	var imageData bytes.Buffer
	if err := png.Encode(&imageData, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	sources := make(map[string]model.Agent)
	unfinished := make(map[string]model.AgentMessage)
	for _, kind := range []string{"pi", "claude", "codex"} {
		created := instance.create(t, map[string]any{"title": kind + " source", "harness": kind, "prompt": "NATIVE_REMEMBER heron_" + kind})
		waitNativeMessage(t, instance.client, created.ID, created.InitialMessage.ID, "remembered:heron_"+kind)
		message := instance.send(t, created.ID, "NATIVE_LIST source_"+kind)
		waitNativeMessage(t, instance.client, created.ID, message.ID, "listed:source_"+kind)
		imageMessage, err := instance.client.SendCompanionImages(t.Context(), created.ID, "NATIVE_IMAGE "+kind, "fork-image-"+kind, []model.ImageAttachment{{Name: "pixel.png", Data: base64.StdEncoding.EncodeToString(imageData.Bytes())}})
		if err != nil {
			t.Fatal(err)
		}
		waitNativeMessage(t, instance.client, created.ID, imageMessage.ID, "image:"+kind)
		sources[kind] = instance.idle(t, created.ID)
		unfinished[kind] = instance.send(t, created.ID, "NATIVE_BLOCK "+kind)
		select {
		case <-modelEndpoint.blocked[kind]:
		case <-time.After(15 * time.Second):
			t.Fatal("source did not enter its active turn")
		}
		deadline := time.Now().Add(15 * time.Second)
		for {
			data, err := os.ReadFile(sources[kind].SessionPath)
			if err == nil && bytes.Contains(data, []byte("NATIVE_BLOCK "+kind)) {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("source did not save its unfinished input")
			}
			time.Sleep(25 * time.Millisecond)
		}
	}
	for _, from := range []string{"pi", "claude", "codex"} {
		for _, to := range []string{"pi", "claude", "codex"} {
			if from == to {
				continue
			}
			t.Run(from+"_to_"+to, func(t *testing.T) {
				source := sources[from]
				fork := instance.create(t, map[string]any{"title": from + " to " + to, "harness": to, "context_agent": source.ID, "prompt": "NATIVE_FORK heron_" + from})
				view := waitNativeMessage(t, instance.client, fork.ID, fork.InitialMessage.ID, "forked:heron_"+from)
				if view.Agent.Harness() != to || view.Agent.SessionID == source.SessionID || view.Agent.ContextAgentID != source.ID {
					t.Fatalf("fork identity: %#v", view.Agent)
				}
				if _, err := os.Stat(filepath.Join(instance.state, "agents", fork.ID, "import", "session.jsonl")); !os.IsNotExist(err) {
					t.Fatalf("converted seed remained after registration: %v", err)
				}
				if to != "pi" {
					instance.killController(t, fork.ID)
					message := instance.send(t, fork.ID, "NATIVE_RECALL heron_"+from)
					resumed := waitNativeMessage(t, instance.client, fork.ID, message.ID, "recalled:heron_"+from)
					if resumed.Agent.SessionID != view.Agent.SessionID {
						t.Fatal("restart replaced the forked conversation")
					}
				}
			})
		}
	}
	for kind := range sources {
		modelEndpoint.mu.Lock()
		count := modelEndpoint.requests["NATIVE_BLOCK "+kind]
		modelEndpoint.mu.Unlock()
		if count != 1 {
			t.Fatalf("unfinished %s input ran %d times; want only the source execution", kind, count)
		}
	}
	releaseSources()
	for kind, message := range unfinished {
		waitNativeMessage(t, instance.client, sources[kind].ID, message.ID, "recovered:"+kind)
	}
}
