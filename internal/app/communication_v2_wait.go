package app

import (
	"context"
	"fmt"

	"github.com/matipan/galpon/internal/model"
)

// awaitCoordinationMessages never holds an HTTP request for child completion.
// It takes already-ready durable receipts and otherwise returns parked state.
// The current operation settles to waiting after Pi finishes the invocation.
func (a *App) awaitCoordinationMessages(ctx context.Context, callerID, runtimeID, operationID string, operationAttempt int, toolRequestID string, ids []string, returnWhen string) (model.AgentWaitManyResult, error) {
	if len(ids) < 1 || len(ids) > 16 {
		return model.AgentWaitManyResult{}, invalidRequestf("message_ids must contain between 1 and 16 items")
	}
	if returnWhen != "any" && returnWhen != "all" {
		return model.AgentWaitManyResult{}, invalidRequestf("return_when must be any or all")
	}
	seen := make(map[string]bool, len(ids))
	messages := make([]model.AgentMessage, len(ids))
	for index, id := range ids {
		if id == "" || seen[id] {
			return model.AgentWaitManyResult{}, invalidRequestf("message IDs must be nonempty and unique")
		}
		seen[id] = true
		message, err := a.Store.AgentMessageForParticipant(ctx, id, callerID)
		if err != nil {
			return model.AgentWaitManyResult{}, err
		}
		if message.SenderAgentID != callerID {
			return model.AgentWaitManyResult{}, invalidRequestf("message is not child work of this agent")
		}
		join, err := a.Store.AgentOperationJoin(ctx, operationID, id)
		if err != nil || join.OperationID != operationID {
			return model.AgentWaitManyResult{}, invalidRequestf("message is not joined to the current operation")
		}
		messages[index] = message
	}
	receiptValues, resultValues, err := a.Store.TakeOperationReceiptsForMessages(ctx, operationID, callerID, runtimeID, operationAttempt, toolRequestID, 64<<10, ids)
	if err != nil {
		return model.AgentWaitManyResult{}, err
	}
	batch := CoordinationReceiptBatch{Receipts: receiptValues, Results: resultValues}
	results := make(map[string]model.AgentMessageResult, len(batch.Results))
	for _, result := range batch.Results {
		results[result.MessageID] = result
	}
	receipts := make(map[string]model.AgentInboxReceipt, len(batch.Receipts))
	for _, receipt := range batch.Receipts {
		receipts[receipt.MessageID] = receipt
	}
	result := model.AgentWaitManyResult{Status: "parked", ReturnWhen: returnWhen, Total: len(messages), Outcomes: make([]model.AgentWaitResult, len(messages))}
	for index, message := range messages {
		outcome := model.AgentWaitResult{AgentMessage: message, MessageID: message.ID, WaitStatus: "pending", MessageStatus: message.Status, TargetRuntimeStatus: "unknown"}
		if target, readErr := a.Store.Agent(ctx, message.TargetAgentID); readErr == nil {
			outcome.TargetRuntimeStatus = target.Status
		}
		if receipt, ok := receipts[message.ID]; ok {
			outcome.ReceiptID = receipt.ID
		}
		if settled, ok := results[message.ID]; ok {
			outcome.MessageStatus = settled.Status
			outcome.AgentMessage.Status = "completed"
			outcome.AgentMessage.Response = settled.Response
			outcome.AgentMessage.Error = settled.Error
			if settled.Status == "completed" {
				outcome.WaitStatus = "completed"
			} else {
				outcome.WaitStatus = "failed"
				outcome.WaitError = &model.AgentWaitError{Kind: "message_failed", Message: boundedWaitFailure(settled.Error)}
			}
			result.Completed++
		}
		result.Outcomes[index] = outcome
	}
	if result.Completed == result.Total || returnWhen == "any" && result.Completed > 0 {
		result.Status = "completed"
	}
	return result, nil
}

func boundedWaitFailure(value string) string {
	if len(value) <= 1000 {
		return value
	}
	return fmt.Sprintf("%s…", value[:999])
}
