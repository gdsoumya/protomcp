package protomcp

import (
	"sync"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// TestRegisterPromptArgCompletions_RaceSafe stresses concurrent registrations
// and completion lookups. Run with `go test -race`, without the mutex on
// promptCompletions, Go's race detector would abort the test with
// "concurrent map read and map write".
func TestRegisterPromptArgCompletions_RaceSafe(t *testing.T) {
	s := New("t", "0")

	var wg sync.WaitGroup
	const N = 64

	for i := range N {
		wg.Add(2)
		go func(i int) {
			defer wg.Done()
			s.RegisterPromptArgCompletions("prompt", "arg", []string{"a", "b", "c"})
			_ = i
		}(i)
		go func() {
			defer wg.Done()
			_ = s.completePromptArg(&mcp.CompleteRequest{Params: &mcp.CompleteParams{
				Ref:      &mcp.CompleteReference{Type: "ref/prompt", Name: "prompt"},
				Argument: mcp.CompleteParamsArgument{Name: "arg", Value: ""},
			}})
		}()
	}
	wg.Wait()
}
