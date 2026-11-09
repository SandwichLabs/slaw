package main

import (
	"context"
	"fmt"
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
	// conv is the id of the conversation the next turn continues: the agent
	// session a run receipt names (receipts.WithAgentSession). The panel names a
	// turn, not a conversation, so the conversation is what the history says: a
	// turn with no history starts one, and a turn with history continues it.
	conv string
}

// conversation is the id of the conversation a turn with this many prior
// messages belongs to: a new one for the first turn, the same one after.
func (c *chatTurns) conversation(historyLen int) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if historyLen == 0 || c.conv == "" {
		c.conv = receipts.NewSessionID()
	}
	return c.conv
}

// endConversation forgets the conversation (the workspace is closing), so the
// next turn starts a new one whatever its history says.
func (c *chatTurns) endConversation() {
	c.mu.Lock()
	c.conv = ""
	c.mu.Unlock()
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
