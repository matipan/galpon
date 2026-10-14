package sessionfile

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const nativeHistoryID = "2f61c202-f117-456b-af20-0c6c757f9b08"

func TestClaudeRetainedMessagesUseCompactionParentLinks(t *testing.T) {
	data := `{"type":"user","sessionId":"` + nativeHistoryID + `","uuid":"old","parentUuid":null,"message":{"role":"user","content":"Archived question"}}
{"type":"assistant","uuid":"old-answer","parentUuid":"old","message":{"role":"assistant","content":"Archived answer"}}
{"type":"user","uuid":"kept","parentUuid":"old-answer","message":{"role":"user","content":"Retained question"}}
{"type":"assistant","uuid":"kept-answer","parentUuid":"kept","message":{"role":"assistant","content":"Retained answer"}}
{"type":"system","subtype":"compact_boundary","uuid":"boundary","parentUuid":null,"logicalParentUuid":"kept-answer","compactMetadata":{"preservedMessages":{"uuids":["kept","kept-answer"],"allUuids":["old","kept","kept-answer"],"anchorUuid":"summary"}}}
{"type":"user","uuid":"summary","parentUuid":"boundary","isCompactSummary":true,"message":{"role":"user","content":"Current summary"}}
{"type":"user","uuid":"new","parentUuid":"kept-answer","message":{"role":"user","content":"New question"}}
{"type":"progress","uuid":"progress","parentUuid":null,"data":{"type":"tool-progress"}}
`
	history := readHistory(t, historyFile(t, data), "claude")
	context := historyMessages(t, history, true)
	want := []string{"Current summary", "Retained question", "Retained answer", "New question"}
	if len(context) != len(want) {
		t.Fatalf("context = %#v", context)
	}
	for i, message := range context {
		if message.Text != want[i] {
			t.Fatalf("context %d = %q, want %q", i, message.Text, want[i])
		}
	}
	archive := historyMessages(t, history, false)
	if len(archive) != 6 || archive[0].Text != "Archived question" || archive[1].Text != "Archived answer" {
		t.Fatalf("archive = %#v", archive)
	}
}

func TestCodexRollbackDiscardsOnlyRemovedTurnContext(t *testing.T) {
	data := `{"type":"session_meta","payload":{"id":"` + nativeHistoryID + `"}}
{"type":"event_msg","payload":{"type":"task_started","turn_id":"one"}}
{"type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"Kept question"}]}}
{"type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Kept answer"}]}}
{"type":"event_msg","payload":{"type":"task_complete","turn_id":"one"}}
{"type":"event_msg","payload":{"type":"task_started","turn_id":"two"}}
{"type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"Removed question"}]}}
{"type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Removed answer"}]}}
{"type":"compacted","payload":{"message":"Removed checkpoint","replacement_history":[{"type":"message","role":"user","content":[{"type":"input_text","text":"Removed checkpoint"}]}]}}
{"type":"event_msg","payload":{"type":"task_complete","turn_id":"two"}}
{"type":"event_msg","payload":{"type":"thread_rolled_back","num_turns":1}}
{"type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"New question"}]}}
`
	history := readHistory(t, historyFile(t, data), "codex")
	context := historyMessages(t, history, true)
	if len(context) != 3 || context[0].Text != "Kept question" || context[1].Text != "Kept answer" || context[2].Text != "New question" {
		t.Fatalf("context = %#v", context)
	}
	archive := historyMessages(t, history, false)
	if len(archive) != 6 || archive[2].Text != "Removed question" || archive[3].Text != "Removed answer" {
		t.Fatalf("archive = %#v", archive)
	}
}

func TestEncryptedCheckpointMarksReadableHistoryFallback(t *testing.T) {
	data := `{"type":"session_meta","payload":{"id":"` + nativeHistoryID + `"}}
{"type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"Readable question"}]}}
{"type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Readable answer"}]}}
{"type":"compacted","payload":{"message":"","replacement_history":[{"type":"compaction","encrypted_content":"opaque-provider-checkpoint"}]}}
`
	history := readHistory(t, historyFile(t, data), "codex")
	context := historyMessages(t, history, true)
	if len(context) != 3 || context[0].Text != "Readable question" || context[1].Text != "Readable answer" || !strings.Contains(context[2].Text, "encrypted") || !strings.Contains(context[2].Text, "not available") {
		t.Fatalf("fallback = %#v", context)
	}
}

func TestForkAttachmentsRemainIndependentOfSourceFiles(t *testing.T) {
	var pixels bytes.Buffer
	if err := png.Encode(&pixels, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	imagePath := filepath.Join(t.TempDir(), "source.png")
	if err := os.WriteFile(imagePath, pixels.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write(pixels.Bytes())
	}))
	t.Cleanup(remote.Close)
	resource := "Text from a saved resource"
	message := map[string]any{"type": "user", "sessionId": nativeHistoryID, "uuid": "result", "parentUuid": nil, "message": map[string]any{"role": "user", "content": []any{map[string]any{"type": "tool_result", "tool_use_id": "unfinished-call", "content": []any{
		map[string]any{"type": "image", "source": map[string]any{"type": "url", "url": "file://" + imagePath}},
		map[string]any{"type": "image", "source": map[string]any{"type": "url", "url": remote.URL + "/image"}},
		map[string]any{"type": "resource", "resource": map[string]any{"uri": "file:///saved.txt", "mimeType": "text/plain", "blob": base64.StdEncoding.EncodeToString([]byte(resource))}},
	}}}}}
	data, err := json.Marshal(message)
	if err != nil {
		t.Fatal(err)
	}
	history := readHistory(t, historyFile(t, string(data)+"\n"), "claude")
	remote.Close()
	images := 0
	for _, message := range historyMessages(t, history, true) {
		for _, part := range message.Parts {
			if part.Type == "image" && part.MIME == "image/png" && part.Data == base64.StdEncoding.EncodeToString(pixels.Bytes()) {
				images++
			}
		}
	}
	if images != 2 {
		t.Fatalf("saved image content = %d images, want local and URL image data", images)
	}
	if err := os.Remove(imagePath); err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{"pi", "claude", "codex"} {
		t.Run(target, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, "import", "session.jsonl")
			if err := WriteHistory(path, target, root, history); err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(path)
			if err != nil || !bytes.Contains(data, []byte(base64.StdEncoding.EncodeToString(pixels.Bytes()))) {
				t.Fatalf("saved image not embedded: %v", err)
			}
			files, err := os.ReadDir(filepath.Join(root, "history"))
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, file := range files {
				if file.Name() == "source.jsonl" {
					continue
				}
				path := filepath.Join(root, "history", file.Name())
				saved, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				if string(saved) == resource && bytes.Contains(data, []byte(path)) {
					found = true
				}
			}
			if !found {
				t.Fatal("resource bytes or link are missing")
			}
		})
	}
}

func TestNativeIdentityAfterLargeMetadataRecord(t *testing.T) {
	data := `{"type":"file-history-snapshot","metadata":"` + strings.Repeat("x", 2<<20) + `"}` + "\n" + `{"type":"user","sessionId":"` + nativeHistoryID + `","message":{"role":"user","content":"hello"}}` + "\n"
	id, err := NativeID("claude", strings.NewReader(data))
	if err != nil || id != nativeHistoryID {
		t.Fatalf("identity = %q, %v", id, err)
	}
}
