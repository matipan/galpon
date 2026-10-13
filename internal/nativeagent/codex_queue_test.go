package nativeagent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/coder/websocket"
	"github.com/matipan/galpon/internal/model"
)

func TestCodexWithdrawalPreservesOtherQueuedWork(t *testing.T) {
	var mu sync.Mutex
	queue := map[string]string{"wanted": "galpon:agent:token", "human": "native-human", "other": "galpon:agent:another-token"}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = conn.CloseNow() }()
		for {
			_, data, err := conn.Read(r.Context())
			if err != nil {
				return
			}
			var request struct {
				ID     json.RawMessage `json:"id"`
				Method string          `json:"method"`
				Params map[string]any  `json:"params"`
			}
			if json.Unmarshal(data, &request) != nil {
				return
			}
			mu.Lock()
			var result any
			switch request.Method {
			case "thread/queue/list":
				entries := []map[string]any{}
				for id, client := range queue {
					entries = append(entries, map[string]any{"id": id, "clientUserMessageId": client})
				}
				result = map[string]any{"data": entries}
			case "thread/queue/delete":
				id := stringValue(request.Params["queuedSubmissionId"])
				_, exists := queue[id]
				delete(queue, id)
				result = map[string]any{"deleted": exists}
			}
			mu.Unlock()
			reply, _ := json.Marshal(map[string]any{"id": request.ID, "result": result})
			if conn.Write(r.Context(), websocket.MessageText, reply) != nil {
				return
			}
		}
	}))
	defer server.Close()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { cancel(); _ = conn.CloseNow() }()
	d := &codexDriver{options: launchOptions{Agent: model.Agent{ID: "agent"}, Events: make(chan nativeEvent, 8)}, id: "thread", connection: conn, closed: make(chan struct{}), pending: make(map[string]chan json.RawMessage)}
	go d.read(ctx)
	removed, err := d.Withdraw(ctx, "token")
	if err != nil || !removed {
		t.Fatalf("withdrawal: %v, %v", removed, err)
	}
	mu.Lock()
	remaining := len(queue)
	human, other := queue["human"], queue["other"]
	mu.Unlock()
	if remaining != 2 || human != "native-human" || other != "galpon:agent:another-token" {
		t.Fatal("withdrawal changed unrelated queued work")
	}
	removed, err = d.Withdraw(ctx, "token")
	if err != nil || removed {
		t.Fatalf("missing submission was treated as safely withdrawn: %v, %v", removed, err)
	}
}
