package protomcp

import (
	"context"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// mcpCompletionLimit is the MCP spec cap of 100 completion values per
// response.
const mcpCompletionLimit = 100

type promptArgKey struct {
	prompt string
	arg    string
}

// RegisterPromptArgCompletions stores known completion values for
// (promptName, argName). Replaces any existing entry for the pair.
func (s *Server) RegisterPromptArgCompletions(promptName, argName string, values []string) {
	if promptName == "" || argName == "" {
		return
	}
	// Copy before taking the lock so later caller mutation cannot
	// corrupt the stored entry.
	dup := make([]string, len(values))
	copy(dup, values)

	s.promptCompletionsMu.Lock()
	defer s.promptCompletionsMu.Unlock()
	if s.promptCompletions == nil {
		s.promptCompletions = map[promptArgKey][]string{}
	}
	s.promptCompletions[promptArgKey{promptName, argName}] = dup
}

// installCompletionHandler wraps opts.CompletionHandler: a
// caller-supplied handler wins when it returns a non-empty result;
// otherwise the generated prompt-argument table answers.
func (s *Server) installCompletionHandler(opts *mcp.ServerOptions) *mcp.ServerOptions {
	if opts == nil {
		opts = &mcp.ServerOptions{}
	}
	prev := opts.CompletionHandler
	opts.CompletionHandler = func(ctx context.Context, req *mcp.CompleteRequest) (*mcp.CompleteResult, error) {
		if prev != nil {
			res, err := prev(ctx, req)
			if err != nil {
				return nil, err
			}
			if res != nil && len(res.Completion.Values) > 0 {
				return res, nil
			}
		}
		return s.completePromptArg(req), nil
	}
	return opts
}

// completePromptArg returns registered completions for the request's
// prompt argument, filtered by prefix and capped at mcpCompletionLimit.
func (s *Server) completePromptArg(req *mcp.CompleteRequest) *mcp.CompleteResult {
	empty := &mcp.CompleteResult{Completion: mcp.CompletionResultDetails{Values: []string{}}}
	if req == nil || req.Params == nil || req.Params.Ref == nil {
		return empty
	}
	if req.Params.Ref.Type != "ref/prompt" {
		return empty
	}
	s.promptCompletionsMu.RLock()
	values, ok := s.promptCompletions[promptArgKey{req.Params.Ref.Name, req.Params.Argument.Name}]
	s.promptCompletionsMu.RUnlock()
	if !ok {
		return empty
	}

	prefix := req.Params.Argument.Value
	matches := make([]string, 0, len(values))
	for _, v := range values {
		if prefix == "" || strings.HasPrefix(v, prefix) {
			matches = append(matches, v)
		}
	}
	total := len(matches)
	hasMore := false
	if total > mcpCompletionLimit {
		matches = matches[:mcpCompletionLimit]
		hasMore = true
	}
	return &mcp.CompleteResult{
		Completion: mcp.CompletionResultDetails{
			Values:  matches,
			Total:   total,
			HasMore: hasMore,
		},
	}
}
