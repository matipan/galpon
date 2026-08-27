package app

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/matipan/galpon/internal/model"
	"github.com/matipan/galpon/internal/piagent"
	"github.com/matipan/galpon/internal/store"
)

const communicationProtocolV2Generation = 2

type CommunicationProtocolState struct {
	Generation  int  `json:"generation"`
	Complete    bool `json:"complete"`
	Maintenance bool `json:"maintenance"`
}

type CommunicationUpgradeRequest struct {
	Generation     int                         `json:"generation"`
	KnownTodoLinks []model.AgentTodoLinkIntent `json:"knownTodoLinks,omitempty"`
	IdleTimeout    time.Duration               `json:"-"`
	BarrierTimeout time.Duration               `json:"-"`
}

type CommunicationUpgradeResult struct {
	Generation         int  `json:"generation"`
	Messages           int  `json:"messages"`
	Operations         int  `json:"operations"`
	Results            int  `json:"results"`
	Receipts           int  `json:"receipts"`
	Joins              int  `json:"joins"`
	TodoLinks          int  `json:"todoLinks"`
	RunningRuntimes    int  `json:"runningRuntimes"`
	RegisteredRuntimes int  `json:"registeredRuntimes"`
	ReadyAgents        int  `json:"readyAgents"`
	BackupVerified     bool `json:"backupVerified"`
}

type CoordinationOperationDelivery struct {
	Operation model.AgentOperation `json:"operation"`
	Message   *model.AgentMessage  `json:"message,omitempty"`
}

type CoordinationReceiptBatch struct {
	Receipts []model.AgentInboxReceipt  `json:"receipts"`
	Results  []model.AgentMessageResult `json:"results"`
}

type DirectOperationRequest struct {
	RuntimeID          string `json:"runtimeId"`
	UserEntryID        string `json:"userEntryId"`
	ProtocolGeneration int    `json:"protocolGeneration"`
}

const (
	maxOperationOwnershipReconcileIDs = 256
	maxCommunicationBarrierAgents     = 3
	maxCommunicationBarrierTitleRunes = 80
)

type OperationOwnershipReconcileResult struct {
	OwnedOperationIDs []string `json:"ownedOperationIds"`
}

// ReconcileOperationOwnership is a Pi-runtime-only visibility query. It does
// not claim, schedule, or mutate work. The caller supplies only replayed local
// association IDs, and the daemon returns the bounded subset that is still in
// an authoritative nonterminal state for this agent.
func (a *App) ReconcileOperationOwnership(ctx context.Context, agentID, runtimeID string, generation int, operationIDs []string) (OperationOwnershipReconcileResult, error) {
	if len(operationIDs) > maxOperationOwnershipReconcileIDs {
		return OperationOwnershipReconcileResult{}, invalidRequestf("operation ownership reconciliation accepts at most %d IDs", maxOperationOwnershipReconcileIDs)
	}
	registered, err := a.Store.AgentRuntimeProtocolGenerationMatches(ctx, agentID, runtimeID, generation)
	if err != nil {
		return OperationOwnershipReconcileResult{}, err
	}
	if !registered {
		return OperationOwnershipReconcileResult{}, invalidRequestf("runtime is not registered for communication protocol generation %d", generation)
	}
	seen := make(map[string]struct{}, len(operationIDs))
	for _, id := range operationIDs {
		id = strings.TrimSpace(id)
		if id == "" || len(id) > 200 {
			return OperationOwnershipReconcileResult{}, invalidRequestf("operation ownership reconciliation has an invalid ID")
		}
		if _, exists := seen[id]; exists {
			return OperationOwnershipReconcileResult{}, invalidRequestf("operation ownership reconciliation IDs must be unique")
		}
		seen[id] = struct{}{}
	}
	owned, err := a.Store.ReconcileAgentOperationOwnership(ctx, agentID, operationIDs)
	return OperationOwnershipReconcileResult{OwnedOperationIDs: owned}, err
}

func (a *App) RecoverCommunicationRuntime(ctx context.Context, agentID, runtimeID string) (store.CommunicationRuntimeRecoveryResult, error) {
	agentID = strings.TrimSpace(agentID)
	runtimeID = strings.TrimSpace(runtimeID)
	if agentID == "" || runtimeID == "" {
		return store.CommunicationRuntimeRecoveryResult{}, invalidRequestf("exact agent and runtime IDs are required")
	}
	if len(agentID) > 200 || len(runtimeID) > 200 {
		return store.CommunicationRuntimeRecoveryResult{}, invalidRequestf("agent or runtime ID exceeds the 200-character limit")
	}
	// Do not take the upgrade mutex here. An operator must be able to recover a
	// confirmed stale runtime while the bounded registration barrier is waiting.
	// The store transaction accepts only the active barrier state.
	return a.Store.RecoverUnregisteredCommunicationRuntime(ctx, agentID, runtimeID)
}

func (a *App) CommunicationProtocolState(ctx context.Context) (CommunicationProtocolState, error) {
	generation, complete, maintenance, err := a.Store.CommunicationProtocolState(ctx)
	if err != nil {
		return CommunicationProtocolState{}, err
	}
	pending, draining, err := a.Store.CommunicationDrainState(ctx)
	if err != nil {
		return CommunicationProtocolState{}, err
	}
	if draining {
		maintenance = true
		if pending > generation {
			generation = pending
		}
	}
	return CommunicationProtocolState{Generation: generation, Complete: complete, Maintenance: maintenance}, nil
}

func (a *App) communicationV2Active(ctx context.Context) (CommunicationProtocolState, error) {
	state, err := a.CommunicationProtocolState(ctx)
	if err != nil {
		return state, err
	}
	return state, nil
}

func (a *App) beginCommunicationMutation(ctx context.Context) (func(), error) {
	a.communicationMutationMu.RLock()
	if err := a.rejectCommunicationAdmission(ctx); err != nil {
		a.communicationMutationMu.RUnlock()
		return func() {}, err
	}
	return a.communicationMutationMu.RUnlock, nil
}

func (a *App) rejectCommunicationAdmission(ctx context.Context) error {
	if a.communicationDraining.Load() {
		return fmt.Errorf("communication protocol is in maintenance mode")
	}
	_, _, maintenance, err := a.Store.CommunicationProtocolState(ctx)
	if err != nil {
		return err
	}
	if maintenance {
		return fmt.Errorf("communication protocol is in maintenance mode")
	}
	return nil
}

// PrepareAutomaticCommunicationUpgrade closes admission before the daemon
// socket starts listening. Existing runtimes can still finish and register
// after the socket becomes available. The durable drain makes this startup
// step repeatable after a stop, restart, or interrupted cutover.
func (a *App) PrepareAutomaticCommunicationUpgrade(ctx context.Context) (bool, error) {
	_, complete, maintenance, err := a.Store.CommunicationProtocolState(ctx)
	if err != nil {
		return false, err
	}
	pending, draining, err := a.Store.CommunicationDrainState(ctx)
	if err != nil {
		return false, err
	}
	recoveryPending, err := a.Store.CommunicationRecoveryPending(ctx)
	if err != nil {
		return false, err
	}
	if complete && !maintenance && !recoveryPending {
		return false, nil
	}
	if !complete && pending != 0 && pending != communicationProtocolV2Generation {
		return false, fmt.Errorf("communication generation %d is already upgrading", pending)
	}
	if !maintenance && !draining && !complete {
		if err := a.Store.BeginCommunicationDrain(ctx, communicationProtocolV2Generation); err != nil {
			return false, err
		}
	}
	a.communicationDraining.Store(true)
	if a.Logger != nil {
		a.Logger.Printf("automatic communication generation %d upgrade prepared", communicationProtocolV2Generation)
	}
	return true, nil
}

// UpgradeCommunicationV2 is the repeatable terminal upgrade and recovery
// command. Every durable drain, maintenance, backfill, barrier, and final
// recovery phase can continue after a timeout or daemon restart.
func (a *App) UpgradeCommunicationV2(ctx context.Context, request CommunicationUpgradeRequest) (CommunicationUpgradeResult, error) {
	a.communicationUpgradeMu.Lock()
	defer a.communicationUpgradeMu.Unlock()
	if request.Generation == 0 {
		request.Generation = communicationProtocolV2Generation
	}
	if request.Generation <= 1 {
		return CommunicationUpgradeResult{}, invalidRequestf("new protocol generation is required")
	}
	if request.IdleTimeout <= 0 {
		request.IdleTimeout = 5 * time.Minute
	}
	if request.BarrierTimeout <= 0 {
		request.BarrierTimeout = 5 * time.Minute
	}
	current, complete, maintenance, err := a.Store.CommunicationProtocolState(ctx)
	if err != nil {
		return CommunicationUpgradeResult{}, err
	}
	pending, draining, err := a.Store.CommunicationDrainState(ctx)
	if err != nil {
		return CommunicationUpgradeResult{}, err
	}
	if complete && current != request.Generation {
		return CommunicationUpgradeResult{}, fmt.Errorf("communication protocol generation %d is already active", current)
	}
	if !complete && pending != 0 && pending != request.Generation {
		return CommunicationUpgradeResult{}, fmt.Errorf("communication generation %d is already upgrading", pending)
	}

	options := store.CommunicationCutoverOptions{
		Generation: request.Generation, MaintenanceConfirmed: true, BackupVerified: true, SafeIdleConfirmed: true,
		KnownTodoLinks: request.KnownTodoLinks,
	}
	if complete && !maintenance {
		cutover, countErr := a.Store.BackfillCommunicationV2(ctx, options)
		if countErr != nil {
			return CommunicationUpgradeResult{}, countErr
		}
		verified, verifyErr := a.Store.VerifiedCommunicationBackup(ctx, request.Generation)
		if verifyErr != nil {
			return CommunicationUpgradeResult{}, verifyErr
		}
		result := communicationUpgradeResult(cutover, verified)
		recoveryPending, recoveryErr := a.Store.CommunicationRecoveryPending(ctx)
		if recoveryErr != nil {
			return result, recoveryErr
		}
		if !recoveryPending {
			return result, nil
		}
		a.communicationDraining.Store(true)
		return a.finishCommunicationRecovery(ctx, request.Generation, result)
	}

	// The durable drain is the authority across daemon restarts. The in-process
	// gate closes every model-start and mutation API before safe-idle is read.
	if !maintenance && !draining {
		if err := a.Store.BeginCommunicationDrain(ctx, request.Generation); err != nil {
			return CommunicationUpgradeResult{}, err
		}
		draining = true
	}
	a.communicationDraining.Store(true)
	// Wait for every mutation or model-start request that passed the old gate.
	// Later requests observe durable drain maintenance and fail before mutation.
	if err := a.waitForCommunicationMutationDrain(ctx, request.IdleTimeout); err != nil {
		return CommunicationUpgradeResult{}, err
	}
	defer func() {
		generation, cutoverComplete, stillMaintenance, stateErr := a.Store.CommunicationProtocolState(context.Background())
		recoveryPending, recoveryErr := a.Store.CommunicationRecoveryPending(context.Background())
		if stateErr == nil && recoveryErr == nil && cutoverComplete && generation == request.Generation && !stillMaintenance && !recoveryPending {
			a.communicationDraining.Store(false)
		}
	}()

	if draining {
		idleDeadline := time.Now().Add(request.IdleTimeout)
		for {
			idle, readErr := a.Store.CommunicationSafeIdle(ctx)
			if readErr != nil {
				return CommunicationUpgradeResult{}, readErr
			}
			if idle.Safe() {
				break
			}
			if time.Now().After(idleDeadline) {
				return CommunicationUpgradeResult{}, fmt.Errorf("communication upgrade could not reach safe idle: %d deliveries, %d operations, and %d busy runtimes remain", idle.DeliveredMessages, idle.ActiveOperations, idle.BusyRuntimes)
			}
			if err := waitContext(ctx, 100*time.Millisecond); err != nil {
				return CommunicationUpgradeResult{}, err
			}
		}
		if err := a.Store.PromoteCommunicationDrain(ctx, request.Generation); err != nil {
			return CommunicationUpgradeResult{}, err
		}
	}
	if !complete {
		if err := a.Store.BeginCommunicationCutover(ctx, request.Generation); err != nil {
			return CommunicationUpgradeResult{}, err
		}
	}

	verified, err := a.Store.VerifiedCommunicationBackup(ctx, request.Generation)
	if err != nil {
		return CommunicationUpgradeResult{}, err
	}
	if !verified {
		if complete {
			return CommunicationUpgradeResult{}, fmt.Errorf("communication upgrade remains in maintenance: no verified pre-migration backup is available")
		}
		if _, err := a.Store.CreateVerifiedCommunicationBackup(ctx, request.Generation); err != nil {
			return CommunicationUpgradeResult{}, err
		}
		verified = true
	}
	cutover, err := a.Store.BackfillCommunicationV2(ctx, options)
	if err != nil {
		return CommunicationUpgradeResult{}, err
	}
	assets, err := piagent.Materialize(a.Config.StateDir)
	if err != nil {
		return CommunicationUpgradeResult{}, err
	}
	now := time.Now()
	if err := os.Chtimes(assets.Extension, now, now); err != nil {
		return CommunicationUpgradeResult{}, fmt.Errorf("touch Pi extension for reload: %w", err)
	}
	a.PiAssets = assets

	result := communicationUpgradeResult(cutover, verified)
	barrierDeadline := time.Now().Add(request.BarrierTimeout)
	for {
		running, registered, countErr := a.Store.RegisteredCommunicationRuntimeCount(ctx, request.Generation)
		if countErr != nil {
			return result, countErr
		}
		result.RunningRuntimes, result.RegisteredRuntimes = running, registered
		if running == registered {
			break
		}
		if time.Now().After(barrierDeadline) {
			expected, currentRegistered, missing, omitted, detailErr := a.Store.UnregisteredCommunicationRuntimes(ctx, request.Generation, maxCommunicationBarrierAgents)
			if detailErr != nil {
				return result, detailErr
			}
			result.RunningRuntimes, result.RegisteredRuntimes = expected, currentRegistered
			return result, communicationRegistrationBarrierError(request.Generation, currentRegistered, expected, missing, omitted)
		}
		if err := waitContext(ctx, 100*time.Millisecond); err != nil {
			return result, err
		}
	}
	if err := a.Store.CompleteCommunicationCutover(ctx, request.Generation); err != nil {
		return result, err
	}
	return a.finishCommunicationRecovery(ctx, request.Generation, result)
}

func communicationRegistrationBarrierError(generation, registered, expected int, missing []store.CommunicationUnregisteredRuntime, omitted int) error {
	parts := make([]string, 0, len(missing))
	commands := make([]string, 0, len(missing))
	for _, value := range missing {
		parts = append(parts, fmt.Sprintf("%q", boundedCommunicationAgentTitle(value.AgentTitle)))
		commands = append(commands, "galpon communication recover-runtime --agent "+shellQuoteCommunicationIdentity(value.AgentID)+" --runtime "+shellQuoteCommunicationIdentity(value.RuntimeID))
	}
	detail := ""
	if len(parts) > 0 {
		detail = ". Unregistered agents: " + strings.Join(parts, ", ")
	}
	if omitted > 0 {
		detail += fmt.Sprintf(" and %d more", omitted)
	}
	guidance := ""
	if len(commands) > 0 {
		guidance = ". Confirm that each runtime is stale, then run the matching exact recovery command: " + strings.Join(commands, " ; ")
	}
	return fmt.Errorf("communication upgrade remains in maintenance: %d of %d expected runtimes registered generation %d%s%s", registered, expected, generation, detail, guidance)
}

func boundedCommunicationAgentTitle(value string) string {
	value = strings.TrimSpace(strings.Map(func(char rune) rune {
		if char < 32 || char == 127 {
			return ' '
		}
		return char
	}, value))
	runes := []rune(value)
	if len(runes) <= maxCommunicationBarrierTitleRunes {
		return value
	}
	return string(runes[:maxCommunicationBarrierTitleRunes-1]) + "…"
}

func shellQuoteCommunicationIdentity(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

func communicationUpgradeResult(cutover store.CommunicationCutoverResult, backupVerified bool) CommunicationUpgradeResult {
	return CommunicationUpgradeResult{
		Generation: cutover.Generation, Messages: cutover.Messages, Operations: cutover.Operations,
		Results: cutover.Results, Receipts: cutover.Receipts, Joins: cutover.Joins,
		TodoLinks: cutover.TodoLinks, BackupVerified: backupVerified,
	}
}

func (a *App) finishCommunicationRecovery(ctx context.Context, generation int, result CommunicationUpgradeResult) (CommunicationUpgradeResult, error) {
	if err := a.Store.RecoverAgentCoordinationState(ctx); err != nil {
		return result, err
	}
	ready, err := a.Store.CoordinationReadyAgentIDs(ctx)
	if err != nil {
		return result, err
	}
	if err := a.Store.MarkCommunicationRecoveryComplete(ctx, generation); err != nil {
		return result, err
	}
	result.ReadyAgents = len(ready)
	a.communicationDraining.Store(false)
	for _, agentID := range ready {
		a.startAgentForCoordinationWake(ctx, agentID)
	}
	return result, nil
}

func (a *App) waitForCommunicationMutationDrain(ctx context.Context, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		if a.communicationMutationMu.TryLock() {
			a.communicationMutationMu.Unlock()
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("communication upgrade remains in durable drain while an earlier runtime mutation finishes")
		}
		if err := waitContext(ctx, 10*time.Millisecond); err != nil {
			return err
		}
	}
}

func waitContext(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (a *App) RegisterRuntimeV2(ctx context.Context, agentID, runtimeID, sessionID, sessionPath string, generation int) (CommunicationProtocolState, error) {
	unlock := a.lockAgentLifecycle(agentID)
	defer unlock()
	if strings.TrimSpace(runtimeID) == "" || strings.TrimSpace(sessionID) == "" {
		return CommunicationProtocolState{}, fmt.Errorf("runtime ID and session ID are required")
	}
	agent, err := a.Store.Agent(ctx, agentID)
	if err != nil {
		return CommunicationProtocolState{}, err
	}
	if agent.SessionID != "" && agent.SessionID != sessionID {
		return CommunicationProtocolState{}, fmt.Errorf("pi session %s does not belong to agent %s", sessionID, agentID)
	}
	if err := a.Store.RegisterPreparedAgentRuntimeProtocol(ctx, agentID, runtimeID, sessionID, sessionPath, generation); err != nil {
		state, _ := a.CommunicationProtocolState(ctx)
		return state, err
	}
	if err := a.reportAgent(ctx, agentID, "idle", ""); err != nil {
		return CommunicationProtocolState{}, err
	}
	return a.CommunicationProtocolState(ctx)
}

func (a *App) RegisterDirectOperation(ctx context.Context, agentID string, request DirectOperationRequest) (model.AgentOperation, error) {
	finishMutation, err := a.beginCommunicationMutation(ctx)
	if err != nil {
		return model.AgentOperation{}, err
	}
	defer finishMutation()
	if request.UserEntryID = strings.TrimSpace(request.UserEntryID); request.UserEntryID == "" || len(request.UserEntryID) > 200 {
		return model.AgentOperation{}, invalidRequestf("a valid Pi user entry ID is required")
	}
	if request.RuntimeID = strings.TrimSpace(request.RuntimeID); request.RuntimeID == "" {
		return model.AgentOperation{}, invalidRequestf("runtime ID is required")
	}
	if err := a.rejectCommunicationAdmission(ctx); err != nil {
		return model.AgentOperation{}, err
	}
	state, err := a.communicationV2Active(ctx)
	if err != nil {
		return model.AgentOperation{}, err
	}
	if !state.Complete {
		return model.AgentOperation{}, fmt.Errorf("communication protocol v2 is not active")
	}
	if request.ProtocolGeneration != state.Generation {
		return model.AgentOperation{}, fmt.Errorf("communication protocol generation %d is stale; current generation is %d", request.ProtocolGeneration, state.Generation)
	}
	sum := sha256.Sum256([]byte(agentID + "\x00" + request.UserEntryID))
	key := fmt.Sprintf("%x", sum[:])
	now := time.Now().UnixMilli()
	operation, err := a.Store.PutAgentOperation(ctx, model.AgentOperation{
		ID: "direct:" + key, AgentID: agentID, Kind: "direct", State: "ready",
		CausalRunID: "direct-run:" + key, UserEntryID: request.UserEntryID,
		CreatedAt: now, UpdatedAt: now, ProtocolGeneration: request.ProtocolGeneration,
	})
	if err != nil {
		return model.AgentOperation{}, err
	}
	claimed, err := a.Store.ClaimAgentOperationByID(ctx, operation.ID, agentID, request.RuntimeID, "direct:"+request.UserEntryID)
	if err != nil {
		return model.AgentOperation{}, err
	}
	if claimed == nil || claimed.ID != operation.ID {
		return model.AgentOperation{}, sql.ErrNoRows
	}
	if claimed.State == "claimed" {
		if err := a.Store.StartAgentOperation(ctx, claimed.ID, agentID, request.RuntimeID, claimed.Attempt); err != nil {
			return model.AgentOperation{}, err
		}
		claimed.State = "running"
	}
	return *claimed, nil
}

func (a *App) ClaimCoordinationOperation(ctx context.Context, agentID, runtimeID, claimKey string, generation int) (*CoordinationOperationDelivery, error) {
	finishMutation, err := a.beginCommunicationMutation(ctx)
	if err != nil {
		return nil, err
	}
	defer finishMutation()
	state, err := a.communicationV2Active(ctx)
	if err != nil {
		return nil, err
	}
	if !state.Complete {
		return nil, fmt.Errorf("communication protocol v2 is not active")
	}
	if generation != state.Generation {
		return nil, fmt.Errorf("communication protocol generation %d is stale; current generation is %d", generation, state.Generation)
	}
	claimKey = strings.TrimSpace(claimKey)
	if claimKey == "" || len(claimKey) > 200 {
		return nil, invalidRequestf("a valid claim ID is required")
	}
	operation, err := a.Store.ClaimAgentOperation(ctx, agentID, runtimeID, claimKey)
	if err != nil {
		return nil, err
	}
	if operation == nil {
		var receipt *model.AgentInboxReceipt
		operation, receipt, err = a.Store.ClaimAgentInboxReceiptOperation(ctx, agentID, runtimeID, claimKey)
		_ = receipt
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		if operation == nil {
			return nil, nil
		}
	}
	out := &CoordinationOperationDelivery{Operation: *operation}
	if operation.ParentMessageID != "" {
		message, readErr := a.Store.AgentMessageForParticipant(ctx, operation.ParentMessageID, agentID)
		if readErr != nil {
			return nil, readErr
		}
		out.Message = &message
	}
	return out, nil
}

func (a *App) ValidateCoordinationOperation(ctx context.Context, agentID, runtimeID, operationID string, attempt, generation int) (model.AgentOperation, error) {
	operation, err := a.Store.AgentOperation(ctx, strings.TrimSpace(operationID))
	if err != nil {
		return model.AgentOperation{}, err
	}
	now := time.Now().UnixMilli()
	if operation.AgentID != agentID || operation.RuntimeID != runtimeID || operation.Attempt != attempt || operation.ProtocolGeneration != generation ||
		(operation.State != "claimed" && operation.State != "running") || operation.LeaseExpiresAt <= now || operation.DeadlineAt > 0 && operation.DeadlineAt <= now {
		return model.AgentOperation{}, sql.ErrNoRows
	}
	registered, err := a.Store.AgentRuntimeProtocolGenerationMatches(ctx, agentID, runtimeID, generation)
	if err != nil {
		return model.AgentOperation{}, err
	}
	if !registered {
		return model.AgentOperation{}, fmt.Errorf("runtime is not registered for communication protocol generation %d", generation)
	}
	return operation, nil
}

func (a *App) StartCoordinationOperation(ctx context.Context, agentID, runtimeID, operationID string, attempt int) error {
	finishMutation, err := a.beginCommunicationMutation(ctx)
	if err != nil {
		return err
	}
	defer finishMutation()
	return a.Store.StartAgentOperation(ctx, operationID, agentID, runtimeID, attempt)
}

func (a *App) RenewCoordinationOperation(ctx context.Context, agentID, runtimeID, operationID string, attempt int) error {
	return a.Store.RenewAgentOperationLease(ctx, operationID, agentID, runtimeID, attempt)
}

func (a *App) SettleCoordinationOperation(ctx context.Context, agentID, runtimeID, operationID string, attempt int, response, failure string) (store.AgentOperationSettleResult, error) {
	if len(response) > crossAgentResultByteLimit {
		return store.AgentOperationSettleResult{}, invalidRequestf("agent response exceeds the %d-byte limit", crossAgentResultByteLimit)
	}
	if len(failure) > crossAgentErrorByteLimit {
		return store.AgentOperationSettleResult{}, invalidRequestf("agent error exceeds the %d-byte limit", crossAgentErrorByteLimit)
	}
	result, err := a.Store.SettleAgentOperation(ctx, operationID, agentID, runtimeID, attempt, response, failure)
	if err == nil && !result.Parked && result.Operation.ParentMessageID != "" {
		a.notifyMessageWaiters(result.Operation.ParentMessageID)
	}
	return result, err
}

func (a *App) TakeCoordinationReceipts(ctx context.Context, agentID, runtimeID, operationID string, attempt int, toolRequestID string) (CoordinationReceiptBatch, error) {
	receipts, results, err := a.Store.TakeOperationReceipts(ctx, operationID, agentID, runtimeID, attempt, toolRequestID, 64<<10)
	return CoordinationReceiptBatch{Receipts: receipts, Results: results}, err
}

func (a *App) queueDirectCoordinationMessage(ctx context.Context, targetID, prompt, idempotencyKey string, images *[]model.ImageAttachment, generation int) (model.AgentMessage, bool, error) {
	if err := a.rejectCommunicationAdmission(ctx); err != nil {
		return model.AgentMessage{}, false, err
	}
	now := time.Now().UnixMilli()
	deadline := now + (7 * 24 * time.Hour).Milliseconds()
	messageID := uuid.NewString()
	if strings.TrimSpace(idempotencyKey) != "" {
		sum := sha256.Sum256([]byte("direct\x00" + strings.TrimSpace(idempotencyKey)))
		messageID = fmt.Sprintf("message:%x", sum[:])
	}
	message := model.AgentMessage{
		ID: messageID, TargetAgentID: targetID, Kind: "request", Act: "request", ResultMode: "notify",
		Prompt: strings.TrimSpace(prompt), Images: images, Status: "queued", IdempotencyKey: strings.TrimSpace(idempotencyKey),
		QueueDeadlineAt: deadline, ProcessingDeadlineAt: deadline, CreatedAt: now, UpdatedAt: now,
	}
	value, fresh, err := a.Store.AdmitCoordinationMessage(ctx, store.CoordinationSendAdmission{Message: message, ProtocolGeneration: generation})
	return value, fresh, err
}

func (a *App) QueueCoordinationMessage(ctx context.Context, callerID, runtimeID, operationID string, operationAttempt, generation int, targetID, prompt, idempotencyKey, act, resultMode string, todoID int64, todoPolicy string) (model.AgentMessage, bool, error) {
	if err := a.rejectCommunicationAdmission(ctx); err != nil {
		return model.AgentMessage{}, false, err
	}
	state, err := a.CommunicationProtocolState(ctx)
	if err != nil {
		return model.AgentMessage{}, false, err
	}
	if !state.Complete || generation != state.Generation {
		return model.AgentMessage{}, false, fmt.Errorf("communication protocol generation %d is stale; current generation is %d", generation, state.Generation)
	}
	prompt, err = validateAgentMessagePrompt(prompt)
	if err != nil {
		return model.AgentMessage{}, false, err
	}
	if callerID == targetID {
		return model.AgentMessage{}, false, invalidRequestf("an agent cannot send work to itself")
	}
	if _, err := a.Store.Agent(ctx, targetID); err != nil {
		return model.AgentMessage{}, false, err
	}
	operation, err := a.Store.AgentOperation(ctx, operationID)
	if err != nil {
		return model.AgentMessage{}, false, err
	}
	if operation.AgentID != callerID || operation.RuntimeID != runtimeID || operation.Attempt != operationAttempt || operation.ProtocolGeneration != generation || (operation.State != "claimed" && operation.State != "running") {
		return model.AgentMessage{}, false, sql.ErrNoRows
	}
	act = strings.ToLower(strings.TrimSpace(act))
	if act == "" {
		act = "request"
	}
	if act != "request" && act != "query" && act != "inform" {
		return model.AgentMessage{}, false, invalidRequestf("message act must be request, query, or inform")
	}
	resultMode = strings.ToLower(strings.TrimSpace(resultMode))
	if act == "inform" {
		resultMode = "none"
	} else if resultMode == "" {
		resultMode = "join"
	}
	if resultMode != "join" && resultMode != "notify" && resultMode != "none" {
		return model.AgentMessage{}, false, invalidRequestf("result mode must be join or notify")
	}
	if todoID > 0 {
		resultMode = "notify"
	}
	now := time.Now().UnixMilli()
	deadline := operation.CreatedAt + (7 * 24 * time.Hour).Milliseconds()
	if operation.DeadlineAt > now && operation.DeadlineAt < deadline {
		deadline = operation.DeadlineAt
	}
	messageID := uuid.NewString()
	if strings.TrimSpace(idempotencyKey) != "" {
		sum := sha256.Sum256([]byte(operationID + "\x00" + strings.TrimSpace(idempotencyKey)))
		messageID = fmt.Sprintf("message:%x", sum[:])
	}
	message := model.AgentMessage{
		ID: messageID, SenderAgentID: callerID, TargetAgentID: targetID, Kind: "request", Act: act,
		ResultMode: resultMode, Prompt: prompt, Status: "queued", IdempotencyKey: strings.TrimSpace(idempotencyKey),
		RunID: operation.CausalRunID, QueueDeadlineAt: deadline, ProcessingDeadlineAt: deadline,
		CreatedAt: now, UpdatedAt: now,
	}
	if caller, readErr := a.Store.Agent(ctx, callerID); readErr == nil {
		message.SenderTitle = caller.Title
	}
	value, fresh, err := a.Store.AdmitCoordinationMessage(ctx, store.CoordinationSendAdmission{
		Message: message, SourceOperation: operationID, OperationAttempt: operationAttempt,
		RuntimeID: runtimeID, ProtocolGeneration: generation, TodoID: todoID,
		TodoPolicy: todoPolicy, JoinDeadlineAt: deadline,
	})
	if err == nil && fresh {
		a.startAgentForCoordinationWake(ctx, targetID)
	}
	return value, fresh, err
}

func (a *App) startAgentForCoordinationWake(ctx context.Context, agentID string) {
	if _, err := a.StartAgent(ctx, agentID); err != nil {
		if a.Logger != nil {
			a.Logger.Printf("start Pi agent %s for coordination wake: %v", agentID, err)
		}
		a.scheduleAgentStartRetry(agentID, "")
	}
}
