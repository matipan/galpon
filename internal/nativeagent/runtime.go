package nativeagent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/matipan/galpon/internal/app"
	"github.com/matipan/galpon/internal/config"
	"github.com/matipan/galpon/internal/model"
	"github.com/matipan/galpon/internal/sessionfile"
)

type observation struct {
	Marker     string
	MessageIDs []string
	Recorded   bool
}

type runtimeJob struct {
	Operation                                model.AgentOperation
	Token, Prompt, NativeID, RootID          string
	ReceiptKey                               string
	DisplayPrompt, DeliveryKind, SenderTitle string
	Receipts                                 app.CoordinationReceiptBatch
	Images                                   []model.ImageAttachment `json:"-"`
	Observations                             []observation
	SentAt, TerminalAt                       int64
	Started, Terminal                        bool
	ResultsObserved                          bool
	Final, Failure                           string
}

type runtimeJournal struct{ Jobs []*runtimeJob }

type runtimeController struct {
	mu                     sync.Mutex
	client                 *app.Client
	agent                  model.Agent
	runtimeID              string
	generation             int
	background             bool
	sessionID, journalPath string
	driver                 driver
	transcript             *transcript
	jobs                   []*runtimeJob
	recovery               map[string]*runtimeJob
	active                 string
	events                 chan nativeEvent
	channel                chan map[string]any
	registered             bool
	sessionSaved           bool
	lastRenew              time.Time
	lastStatus             string
}

// Run uses a private controller socket and the existing daemon protocol. It does
// not install binaries or write user harness settings.
func Run(ctx context.Context, cfg config.Config, agentID, preparedRuntimeID string, background bool, input io.Reader, output, errorOutput io.Writer) (runError error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	client := app.NewClient(cfg.Socket)
	view, err := client.Agent(ctx, agentID)
	if err != nil {
		return err
	}
	agent := view.Agent
	if agent.Harness() != model.HarnessClaude && agent.Harness() != model.HarnessCodex {
		return fmt.Errorf("native runtime requires a Claude Code or Codex agent")
	}
	root := filepath.Join(cfg.StateDir, "agents", agent.ID)
	if agent.SessionPath == "" && agent.SessionID != "" {
		path := filepath.Join(root, "sessions", agent.SessionID+".jsonl")
		if info, err := os.Stat(path); err == nil && info.Size() > 0 {
			agent.SessionPath = path
		}
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return err
	}
	lock, err := os.OpenFile(filepath.Join(root, "native.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer func() { _ = lock.Close() }()
	if err := acquireSessionLock(ctx, lock, writerOwnerPath(root)); err != nil {
		return err
	}
	temp, err := os.MkdirTemp("/tmp", "galpon-native-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(temp) }()
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	dashboard, err := client.Dashboard(ctx)
	if err != nil {
		return err
	}
	directory := agent.Placement.CWD
	if primary, ok := dashboard.PrimaryWorktree(agent); ok {
		directory = primary.Path
	}
	if directory == "" {
		return fmt.Errorf("agent has no working directory")
	}
	source, _ := dashboard.Agent(agent.ContextAgentID)
	if agent.SessionPath == "" && source.SessionPath == "" {
		path := filepath.Join(root, "import", "session.jsonl")
		if file, err := os.Open(path); err == nil {
			source.SessionID, err = sessionfile.NativeID(agent.Harness(), file)
			_ = file.Close()
			if err != nil {
				return err
			}
			source.SessionPath = path
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	id := preparedRuntimeID
	if id == "" {
		id = uuid.NewString()
		if err := client.PrepareRuntime(ctx, agent.ID, id); err != nil {
			return err
		}
	}
	defer func() {
		stopContext, stop := context.WithTimeout(context.Background(), 3*time.Second)
		defer stop()
		failure := ""
		if runError != nil && !errors.Is(runError, context.Canceled) {
			failure = runError.Error()
		}
		_ = client.StopRuntime(stopContext, agent.ID, id, failure)
	}()
	protocol, err := client.CommunicationProtocol(ctx)
	if err != nil {
		return err
	}
	if !protocol.Complete || protocol.Maintenance || protocol.Generation < 3 {
		return fmt.Errorf("native harnesses require active communication protocol generation 3")
	}
	controller := &runtimeController{client: client, agent: agent, runtimeID: id, generation: protocol.Generation,
		background: background, sessionID: agent.SessionID, journalPath: filepath.Join(root, "native-journal.json"),
		events: make(chan nativeEvent, 128), channel: make(chan map[string]any, 1), recovery: make(map[string]*runtimeJob)}
	if data, err := os.ReadFile(controller.journalPath); err == nil {
		var journal runtimeJournal
		if err := json.Unmarshal(data, &journal); err != nil {
			return fmt.Errorf("read native delivery journal: %w", err)
		}
		for _, job := range journal.Jobs {
			if job.Operation.ID != "" {
				controller.recovery[job.Operation.ID] = job
			}
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	socket := filepath.Join(temp, "control.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		return err
	}
	if err := os.Chmod(socket, 0o600); err != nil {
		_ = listener.Close()
		return err
	}
	server := &http.Server{Handler: controller.handler(ctx), ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = server.Serve(listener) }()
	defer func() { _ = server.Close() }()
	options := launchOptions{Config: cfg, Agent: agent, Source: source, Worktrees: view.Worktrees, Directory: directory, Socket: socket, TempDir: temp,
		Executable: executable, Background: background, SessionLock: lock, Events: controller.events, Channel: controller.channel, Input: input, Output: output, Errors: errorOutput,
		Environment: append(os.Environ(), "GALPON_SOCKET="+cfg.Socket, "GALPON_AGENT_ID="+agent.ID, "GALPON_RUNTIME_ID="+id, "GALPON_NATIVE_HARNESS="+agent.Harness())}
	var runtimeDriver driver
	switch agent.Harness() {
	case model.HarnessClaude:
		runtimeDriver, err = startClaude(ctx, options)
	case model.HarnessCodex:
		runtimeDriver, err = startCodex(ctx, options)
	}
	if err != nil {
		return err
	}
	defer runtimeDriver.Close()
	sessionID, sourcePath := runtimeDriver.Session()
	mirror := filepath.Join(root, "sessions", sessionID+".jsonl")
	controller.mu.Lock()
	controller.driver, controller.sessionID = runtimeDriver, sessionID
	controller.transcript = newTranscript(agent.Harness(), sourcePath, mirror)
	controller.mu.Unlock()
	if err := controller.transcript.refresh(); err != nil {
		return err
	}
	registeredPath := ""
	if controller.transcript.offset > 0 {
		registeredPath = mirror
		controller.sessionSaved = true
	}
	if _, err := client.RegisterRuntime(ctx, agent.ID, id, sessionID, registeredPath, protocol.Generation); err != nil {
		return err
	}
	controller.mu.Lock()
	controller.registered = true
	controller.mu.Unlock()
	processPath := filepath.Join(root, "native-process.json")
	if err := atomicJSON(processPath, map[string]any{"pid": os.Getpid(), "runtimeId": id}); err != nil {
		return err
	}
	defer func() { _ = os.Remove(processPath) }()
	terminal := make(chan error, 1)
	go func() { terminal <- runtimeDriver.Terminal(ctx) }()
	if background {
		go func() { _, _ = io.Copy(io.Discard, input); cancel() }()
	}
	wake := make(chan struct{}, 1)
	go func() {
		fingerprint := ""
		for ctx.Err() == nil {
			value, err := client.WaitRuntimeCoordination(ctx, agent.ID, id, fingerprint, protocol.Generation)
			if err != nil {
				select {
				case <-ctx.Done():
					return
				case <-time.After(time.Second):
				}
				continue
			}
			fingerprint = value.Fingerprint
			select {
			case wake <- struct{}{}:
			default:
			}
		}
	}()
	return controller.loop(ctx, terminal, wake)
}

func (c *runtimeController) save() error {
	jobs := append([]*runtimeJob(nil), c.jobs...)
	for _, job := range c.recovery {
		jobs = append(jobs, job)
	}
	return atomicJSON(c.journalPath, runtimeJournal{Jobs: jobs})
}

func (c *runtimeController) loop(ctx context.Context, terminal <-chan error, wake <-chan struct{}) error {
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err := <-terminal:
			return err
		case event := <-c.events:
			if event.Kind == "fatal" {
				return errors.New(event.Failure)
			}
			c.mu.Lock()
			err := c.event(event)
			c.mu.Unlock()
			if err != nil {
				return err
			}
		case <-wake:
		case <-timer.C:
		}
		c.mu.Lock()
		err := c.step(ctx)
		delay := 15 * time.Second
		if len(c.jobs) > 0 {
			delay = 100 * time.Millisecond
		}
		c.mu.Unlock()
		if err != nil {
			return err
		}
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		timer.Reset(delay)
	}
}

func (c *runtimeController) event(event nativeEvent) error {
	if event.ID == "" {
		return nil
	}
	var job *runtimeJob
	for _, candidate := range c.jobs {
		if candidate.NativeID == event.ID || candidate.Token == event.ID {
			job = candidate
			break
		}
		if event.Kind == "input" && candidate.Token != "" && strings.Contains(event.Input, deliveryMarker(candidate.Token)) {
			job = candidate
			break
		}
	}
	if event.Kind == "start" {
		c.active = event.ID
		return nil
	}
	if event.Kind == "input" {
		if job == nil {
			if strings.Contains(event.Input, "[Galpon delivery: ") {
				return fmt.Errorf("native input belongs to an expired Galpon delivery; stop this runtime")
			}
			job = &runtimeJob{NativeID: event.ID, Prompt: event.Input, SentAt: time.Now().UnixMilli()}
			c.jobs = append(c.jobs, job)
		}
		job.NativeID, c.active = event.ID, event.ID
		return c.save()
	}
	if event.Kind == "finish" && job != nil {
		job.Terminal, job.Final, job.Failure = true, event.Final, event.Failure
		job.TerminalAt = time.Now().UnixMilli()
		if c.active == event.ID {
			c.active = ""
		}
		return c.save()
	}
	return nil
}

func (c *runtimeController) evidence(job *runtimeJob) *turnEvidence {
	valid := func(turn *turnEvidence) bool {
		return turn != nil && (job.Token == "" || turn.hasInput(job.Prompt))
	}
	if job.RootID != "" {
		turn := c.transcript.turns[job.RootID]
		if valid(turn) {
			return turn
		}
		return nil
	}
	for _, key := range c.transcript.order {
		turn := c.transcript.turns[key]
		if valid(turn) && (job.Token != "" && turn.hasInput(deliveryMarker(job.Token)) ||
			job.NativeID != "" && (turn.ID == job.NativeID || turn.PromptID == job.NativeID)) {
			job.RootID = turn.ID
			return turn
		}
	}
	return nil
}

func (c *runtimeController) step(ctx context.Context) error {
	if err := c.transcript.refresh(); err != nil {
		return err
	}
	if !c.sessionSaved && c.transcript.offset > 0 {
		if _, err := c.client.RegisterRuntime(ctx, c.agent.ID, c.runtimeID, c.sessionID, c.transcript.mirror, c.generation); err != nil {
			return err
		}
		c.sessionSaved, c.lastStatus = true, ""
	}
	if err := c.flushConversation(ctx); err != nil {
		return err
	}
	stopAfterSettlement := false
	for index := 0; index < len(c.jobs); {
		job := c.jobs[index]
		proof := c.evidence(job)
		if proof != nil && proof.Input != "" {
			if job.Operation.ID == "" {
				operation, err := c.client.RegisterDirectOperation(ctx, c.agent.ID, app.DirectOperationRequest{RuntimeID: c.runtimeID, UserEntryID: c.agent.Harness() + ":" + proof.ID, ProtocolGeneration: c.generation})
				if err != nil {
					return err
				}
				job.Operation, job.Started = operation, true
			}
			if !job.Started {
				if err := c.client.StartCoordinationOperation(ctx, c.agent.ID, job.Operation.ID, c.runtimeID, job.Operation.Attempt); err != nil {
					return err
				}
				for _, receipt := range job.Receipts.Receipts {
					if receipt.State == "acknowledged" {
						continue
					}
					if err := c.client.PresentCoordinationReceipt(ctx, c.agent.ID, job.Operation.ID, receipt.ID, c.runtimeID, job.Operation.Attempt, receiptKey(job)); err != nil {
						return err
					}
				}
				job.Started = true
			}
			if !job.ResultsObserved && len(job.Receipts.Results) > 0 {
				ids := make([]string, 0, len(job.Receipts.Results))
				for _, result := range job.Receipts.Results {
					ids = append(ids, result.MessageID)
				}
				if err := c.client.ObserveRuntimeResults(ctx, c.agent.ID, job.Operation.ID, c.runtimeID, job.Operation.Attempt, "boundary:"+job.Token, ids); err != nil {
					return err
				}
				job.ResultsObserved = true
			}
			if proof.Failure != "" {
				job.Terminal, job.Failure = true, proof.Failure
			}
			for i := range job.Observations {
				observation := &job.Observations[i]
				if observation.Recorded || !proof.ToolProofs[observation.Marker] {
					continue
				}
				if err := c.client.ObserveRuntimeResults(ctx, c.agent.ID, job.Operation.ID, c.runtimeID, job.Operation.Attempt, observation.Marker, observation.MessageIDs); err != nil {
					return err
				}
				observation.Recorded = true
			}
		}
		if proof == nil && job.SentAt > 0 && c.active != "" && c.active != job.NativeID && c.active != job.Token {
			job.SentAt = time.Now().UnixMilli()
		}
		if job.Terminal && job.TerminalAt == 0 {
			job.TerminalAt = time.Now().UnixMilli()
		}
		inputExpired := job.SentAt > 0 && proof == nil && time.Since(time.UnixMilli(job.SentAt)) > time.Minute
		finalExpired := job.Terminal && job.Failure == "" && (proof == nil || !proof.Complete || job.Final != "" && job.Final != proof.Final) && time.Since(time.UnixMilli(job.TerminalAt)) > time.Minute
		if inputExpired || finalExpired {
			// A channel write or queue admission cannot be revoked with certainty.
			// If withdrawal loses that race, stop the writer before reporting failure.
			if job.Token != "" && c.driver != nil {
				withdrawn, _ := c.driver.Withdraw(ctx, job.Token)
				if !withdrawn {
					c.registered = false
					c.driver.Close()
					stopAfterSettlement = true
				}
			}
			job.Terminal, job.Final, job.Failure = true, "", "The native harness did not save the delivery. For Claude Code, check channel availability and accept the local development channel confirmation."
			if finalExpired {
				job.Failure = "The native turn ended without matching saved completion evidence"
			}
		}
		if job.Terminal && job.Operation.ID == "" && job.Failure != "" {
			c.removeJob(index)
			if err := c.save(); err != nil {
				return err
			}
			continue
		}
		settle := job.Terminal && job.Operation.ID != "" && (job.Failure != "" || proof != nil && proof.Complete && (job.Final == "" || job.Final == proof.Final))
		if settle {
			if job.Failure == "" {
				job.Final = proof.Final
				if strings.TrimSpace(job.Final) == "" {
					job.Failure = "The native turn ended without a final text response"
				}
			}
			if err := c.save(); err != nil {
				return err
			}
			_, err := c.client.SettleCoordinationOperation(ctx, c.agent.ID, job.Operation.ID, c.runtimeID, job.Operation.Attempt, job.Final, job.Failure)
			if err != nil {
				return err
			}
			c.removeJob(index)
			if err := c.save(); err != nil {
				return err
			}
			continue
		}
		index++
	}
	if stopAfterSettlement {
		return fmt.Errorf("native writer stopped after a delivery evidence timeout")
	}
	renew := time.Since(c.lastRenew) > 5*time.Second
	if renew {
		for _, job := range c.jobs {
			if job.Operation.ID == "" {
				continue
			}
			if err := c.client.RenewCoordinationOperation(ctx, c.agent.ID, job.Operation.ID, c.runtimeID, job.Operation.Attempt); err != nil {
				return err
			}
		}
		c.lastRenew = time.Now()
	}
	status := "idle"
	if len(c.jobs) > 0 || c.active != "" {
		status = "running"
	}
	if renew || status != c.lastStatus {
		if err := c.client.RuntimeStatus(ctx, c.agent.ID, c.runtimeID, status, ""); err != nil {
			return err
		}
		c.lastStatus = status
	}
	if len(c.jobs) == 0 && c.active == "" {
		return c.claim(ctx)
	}
	return c.save()
}

func (c *runtimeController) removeJob(index int) {
	job := c.jobs[index]
	if c.active == job.NativeID || c.active == job.Token {
		c.active = ""
	}
	c.jobs = append(c.jobs[:index], c.jobs[index+1:]...)
}

func deliveryMarker(token string) string { return "[Galpon delivery: " + token + "]" }

func receiptKey(job *runtimeJob) string {
	if job.ReceiptKey != "" {
		return job.ReceiptKey
	}
	return job.Token
}

func (c *runtimeController) claim(ctx context.Context) error {
	claimID := uuid.NewString()
	delivery, err := c.client.ClaimCoordinationOperation(ctx, c.agent.ID, c.runtimeID, claimID, c.generation)
	if err != nil || delivery == nil {
		return err
	}
	job := &runtimeJob{Operation: delivery.Operation, Token: uuid.NewString()}
	if recovered := c.recovery[delivery.Operation.ID]; recovered != nil {
		delete(c.recovery, delivery.Operation.ID)
		if recovered.Terminal && recovered.Failure == "" {
			job = recovered
			job.Operation, job.Started, job.ResultsObserved = delivery.Operation, false, false
			for i := range job.Observations {
				job.Observations[i].Recorded = false
			}
		}
	}
	batch, err := c.client.TakeCoordinationReceipts(ctx, c.agent.ID, job.Operation.ID, c.runtimeID, job.Operation.Attempt, job.Token)
	if err != nil {
		return err
	}
	job.ReceiptKey = job.Token
	if job.Terminal {
		known := make(map[string]bool)
		for _, receipt := range job.Receipts.Receipts {
			known[receipt.ID] = true
		}
		for _, receipt := range batch.Receipts {
			if !known[receipt.ID] {
				job.Terminal, job.Final, job.RootID, job.NativeID = false, "", "", ""
				job.Token = uuid.NewString()
				break
			}
		}
	}
	job.Receipts = batch
	if !job.Terminal {
		parts := []string{runtimeInstructions(c.agent), deliveryMarker(job.Token)}
		if delivery.Message != nil {
			if delivery.Message.Images != nil {
				job.Images = *delivery.Message.Images
			}
			job.DisplayPrompt, job.DeliveryKind, job.SenderTitle = delivery.Message.Prompt, delivery.Message.Act, delivery.Message.SenderTitle
			parts = append(parts, fmt.Sprintf("Message from %s (%s):\n%s", delivery.Message.SenderTitle, delivery.Message.Act, delivery.Message.Prompt))
		}
		if delivery.Message == nil && len(batch.Receipts) == 0 {
			entryID := strings.TrimPrefix(delivery.Operation.UserEntryID, c.agent.Harness()+":")
			if previous := c.transcript.turns[entryID]; previous != nil && previous.Input != "" {
				parts = append(parts, "Continue this saved user task:\n"+previous.Input)
			} else {
				job.Terminal, job.Failure = true, "The saved native input for this operation is unavailable"
			}
		}
		for _, receipt := range batch.Receipts {
			if receipt.ResultID == "" {
				parts = append(parts, fmt.Sprintf("Coordination notice (%s) for %s. Use galpon_read_message to inspect it within the original task's scope.", receipt.Kind, receipt.MessageID))
			}
		}
		for _, result := range batch.Results {
			parts = append(parts, fmt.Sprintf("Result for %s (%s):\n%s\n%s\nThis is a result notification. Continue the original task within its authorized scope.", result.MessageID, result.Status, result.Response, result.Error))
		}
		job.Prompt = strings.Join(parts, "\n\n")
	}
	c.jobs = append(c.jobs, job)
	if err := c.save(); err != nil {
		return err
	}
	if job.Terminal {
		return nil
	}
	return c.submit(ctx, job)
}

func (c *runtimeController) submit(ctx context.Context, job *runtimeJob) error {
	id, err := c.driver.Submit(ctx, nativeInput{Text: job.Prompt, Images: job.Images}, job.Token)
	if err != nil {
		// Admission can succeed even when its reply is lost. Fence the writer
		// before a submission error becomes a terminal Galpon result.
		_, _ = c.driver.Withdraw(ctx, job.Token)
		c.registered = false
		c.driver.Close()
		job.Terminal, job.Failure = true, err.Error()
		if saveErr := c.save(); saveErr != nil {
			return saveErr
		}
		if _, settleErr := c.client.SettleCoordinationOperation(ctx, c.agent.ID, job.Operation.ID, c.runtimeID, job.Operation.Attempt, "", job.Failure); settleErr != nil {
			return settleErr
		}
		for index, candidate := range c.jobs {
			if candidate == job {
				c.removeJob(index)
				break
			}
		}
		if saveErr := c.save(); saveErr != nil {
			return saveErr
		}
		return fmt.Errorf("submit native delivery: %w", err)
	}
	job.NativeID, job.SentAt, c.active = id, time.Now().UnixMilli(), id
	return c.save()
}

func (c *runtimeController) handler(ctx context.Context) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /channel", func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-ctx.Done():
			http.Error(w, "runtime stopped", http.StatusServiceUnavailable)
		case <-r.Context().Done():
		case value := <-c.channel:
			token := stringValue(objectValue(value["meta"])["request_id"])
			c.mu.Lock()
			live := false
			for _, job := range c.jobs {
				if job.Token == token && !job.Terminal {
					live = true
					break
				}
			}
			c.mu.Unlock()
			if live {
				_ = json.NewEncoder(w).Encode(value)
			} else {
				_, _ = io.WriteString(w, "null\n")
			}
		case <-time.After(20 * time.Second):
			_, _ = io.WriteString(w, "null\n")
		}
	})
	mux.HandleFunc("POST /hook", func(w http.ResponseWriter, r *http.Request) {
		var value map[string]any
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<20)).Decode(&value); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		c.mu.Lock()
		sessionID := c.sessionID
		c.mu.Unlock()
		if id := stringValue(value["session_id"]); sessionID != "" && id != sessionID {
			c.mu.Lock()
			c.registered = false
			c.mu.Unlock()
			emit(ctx, c.events, nativeEvent{Kind: "fatal", Failure: "The native session changed. Use Galpon to create or open another agent."})
			http.Error(w, "native session does not belong to this agent", http.StatusConflict)
			return
		}
		event := nativeEvent{ID: stringValue(value["prompt_id"])}
		switch value["hook_event_name"] {
		case "UserPromptSubmit":
			event.Kind, event.Input = "input", stringValue(value["prompt"])
		case "Stop":
			event.Kind, event.Final = "finish", stringValue(value["last_assistant_message"])
		case "StopFailure":
			event.Kind, event.Failure = "finish", "Claude Code failed to finish the turn"
		}
		if event.Kind != "" {
			emit(ctx, c.events, event)
		}
		_, _ = io.WriteString(w, "{}\n")
	})
	mux.HandleFunc("POST /tool", c.tool)
	return mux
}

func (c *runtimeController) tool(w http.ResponseWriter, r *http.Request) {
	var call bridgeCall
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<20)).Decode(&call); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	allowed := false
	for _, spec := range tools() {
		if call.Name == spec.Name {
			allowed = true
			break
		}
	}
	if !allowed || call.RequestID == "" || len(call.RequestID) > 200 {
		http.Error(w, "invalid native tool request", 400)
		return
	}
	if _, exists := call.Arguments["todo_id"]; exists {
		http.Error(w, "native task lists do not create Galpon TODO links", 400)
		return
	}
	var job *runtimeJob
	deadline := time.NewTimer(10 * time.Second)
	defer deadline.Stop()
	for {
		c.mu.Lock()
		live := c.registered && c.driver != nil
		if live {
			id, _ := c.driver.Session()
			live = id == c.sessionID
		}
		for _, candidate := range c.jobs {
			if live && candidate.Started && !candidate.Terminal && candidate.NativeID == c.active {
				job = candidate
				break
			}
		}
		c.mu.Unlock()
		if job != nil {
			break
		}
		select {
		case <-r.Context().Done():
			return
		case <-deadline.C:
			http.Error(w, "no saved active native turn owns this tool call", http.StatusConflict)
			return
		case <-time.After(20 * time.Millisecond):
		}
	}
	c.mu.Lock()
	operation := job.Operation
	c.mu.Unlock()
	name := strings.TrimPrefix(call.Name, "galpon_")
	value, err := c.client.RuntimeTool(r.Context(), name, app.RuntimeToolRequest{AgentID: c.agent.ID, RuntimeID: c.runtimeID, RequestID: call.RequestID,
		ToolCallID: call.RequestID, OperationID: operation.ID, OperationAttempt: operation.Attempt, ProtocolGeneration: c.generation, Args: call.Arguments})
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	text := string(value)
	if ids := observationIDs(name, call.Arguments, value); len(ids) > 0 {
		marker := uuid.NewString()
		c.mu.Lock()
		job.Observations = append(job.Observations, observation{Marker: marker, MessageIDs: ids})
		err = c.save()
		c.mu.Unlock()
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		text += "\n" + receiptPrefix + marker + "]"
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"content": []any{map[string]any{"type": "text", "text": text}}})
}
