// Package mcpserver is aenotes' MCP tool set: what an AI agent can read and
// do with pages, boards and folders. It is transport-free — the HTTP server
// mounts it at /mcp (internal/server) — and runs every call as the person
// the agent acts for (Caller), with that person's rights.
package mcpserver

import (
	"context"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/aenix-io/aenotes/internal/boards"
)

// Caller is who a tool call acts for.
type Caller struct {
	// Login authors the commits the call leads to ("" when anonymous).
	Login string
	// CanWrite is false for someone with read access to the board
	// repository only: write tools refuse.
	CanWrite bool
}

type callerKey struct{}

// WithCaller stamps the caller on a tool call's context (the transport's
// middleware does it, from the verified token or the local identity).
func WithCaller(ctx context.Context, c Caller) context.Context {
	return context.WithValue(ctx, callerKey{}, c)
}

// CallerFrom is the call's caller; a context without one may only read.
func CallerFrom(ctx context.Context) Caller {
	c, _ := ctx.Value(callerKey{}).(Caller)
	return c
}

// Deps is what the tools need from the server.
type Deps struct {
	Store *boards.Store
	// Live sends a document's update to everyone who has it open (the
	// server's live rooms); the store already holds it.
	Live func(ctx context.Context, id string, update []byte)
	// BaseURL is the public origin, for links back to documents.
	BaseURL string
	Version string
}

// New builds the MCP server with every tool.
func New(d Deps) *mcp.Server {
	s := mcp.NewServer(&mcp.Implementation{Name: "aenotes", Version: d.Version}, &mcp.ServerOptions{
		Instructions: instructions,
	})
	registerTools(s, d)
	s.AddReceivingMiddleware(recoverPanics)
	return s
}

// recoverPanics turns a panic in a tool into that call's error: the SDK
// runs handlers on goroutines of its own, where a panic would end the
// whole server.
func recoverPanics(next mcp.MethodHandler) mcp.MethodHandler {
	return func(ctx context.Context, method string, req mcp.Request) (res mcp.Result, err error) {
		defer func() {
			if p := recover(); p != nil {
				res, err = nil, fmt.Errorf("internal error in %s: %v", method, p)
			}
		}()
		return next(ctx, method, req)
	}
}

const instructions = `aenotes holds pages (rich-text documents) and boards (whiteboards), arranged in folders. ` +
	`Start with list_documents to see the tree, read_document to read one as Markdown, and the create/edit tools to change things. ` +
	`Everything you change shows at once for the people who have it open and is committed to git in your user's name.`
