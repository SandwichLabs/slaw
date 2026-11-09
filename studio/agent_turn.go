package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"slices"
	"sync"

	"github.com/SandwichLabs/slaw/internal/receipts"
)

// chatTurns tracks the one in-flight AgentChatStream turn of the open
// workspace, so the person can cancel it (CancelAgentQuery) and a workspace
// close or switch can stop it before the session goes away. It is deliberately
// separate from taskCancel: closing the AI-cell dialog must never be able to
// cancel a chat turn, nor the reverse.
type chatTurns struct {
	mu     sync.Mutex
	id     string
	cancel context.CancelFunc
	// issued are the conversation ids this open workspace has handed out, oldest
	// first: the agent session a run receipt names (receipts.WithAgentSession) is
	// one of them. Go mints the id and the panel sends it back on the next turn;
	// an id Go did not issue here starts a new conversation, so a panel (or a
	// caller of the binding) cannot name its way into another conversation's
	// receipts.
	issued []string
}

// maxIssuedConversations bounds the ids kept per open workspace: the newest ones.
const maxIssuedConversations = 32

// conversation is the id of the conversation a turn belongs to: id when Go
// issued it in this workspace, otherwise a new one (the first turn, a chat the
// person cleared, a stale panel, an id from elsewhere).
func (c *chatTurns) conversation(id string) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if id != "" && slices.Contains(c.issued, id) {
		return id
	}
	id = receipts.NewSessionID()
	c.issued = append(c.issued, id)
	if len(c.issued) > maxIssuedConversations {
		c.issued = slices.Clone(c.issued[len(c.issued)-maxIssuedConversations:])
	}
	return id
}

// endConversation forgets every id (the workspace is closing), so no id the
// panel kept is a conversation any more.
func (c *chatTurns) endConversation() {
	c.mu.Lock()
	c.issued = nil
	c.mu.Unlock()
}

// taskSession is the agent session of the AI cell dialog behind taskID: the
// dialog mints one id when it opens and every follow-up of that dialog sends it
// again, so the dialog is one session. It is prefixed, so it can never be a chat
// conversation's id, and a long id is shortened to fit a receipt.
func taskSession(taskID string) string {
	const prefix = "task:"
	if len(taskID) <= receipts.MaxSessionBytes-len(prefix) {
		return prefix + taskID
	}
	sum := sha256.Sum256([]byte(taskID))
	return prefix + hex.EncodeToString(sum[:16])
}

// begin registers turnID with a context derived from parent. It refuses a
// second concurrent turn: a retry of a turn that is still mutating the
// workspace is exactly the duplicate-side-effect bug this guards against.
func (c *chatTurns) begin(parent context.Context, turnID string) (context.Context, func(), error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cancel != nil {
		return nil, nil, fmt.Errorf("an assistant turn is already running; cancel it or wait for it to finish")
	}
	ctx, cancel := context.WithCancel(parent)
	c.id, c.cancel = turnID, cancel
	return ctx, func() {
		c.mu.Lock()
		if c.id == turnID {
			c.id, c.cancel = "", nil
		}
		c.mu.Unlock()
		cancel()
	}, nil
}

// cancelTurn cancels the turn registered under turnID ("" cancels whichever is
// running). A no-op when none matches (the turn usually just finished).
func (c *chatTurns) cancelTurn(turnID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cancel != nil && (turnID == "" || c.id == turnID) {
		c.cancel()
	}
}

// CancelAgentQuery stops the chat turn running under turnID. Tool execution
// halts before the next step and any approval card the turn is waiting on is
// answered "cancelled". A no-op if the turn already finished.
func (a *App) CancelAgentQuery(turnID string) {
	a.chat.cancelTurn(turnID)
}

// cancelAgentWork stops the in-flight chat turn and answers every pending
// approval. Called BEFORE taking a.mu.Lock(), so a turn holding a read lock
// (a long query) is interrupted instead of making the close wait behind it.
func (a *App) cancelAgentWork() {
	a.chat.cancelTurn("")
	a.chat.endConversation()
	a.runs.cancelAll()
	if a.approvals != nil {
		a.approvals.CancelAll()
	}
}
