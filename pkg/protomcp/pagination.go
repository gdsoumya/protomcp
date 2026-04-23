package protomcp

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// InvalidCursorError wraps a pagination-cursor decoding failure so the
// error handler can route it to JSON-RPC -32602 Invalid params.
type InvalidCursorError struct{ Err error }

// Error implements error.
func (e *InvalidCursorError) Error() string { return "invalid cursor: " + e.Err.Error() }

// Unwrap returns the underlying decoder error.
func (e *InvalidCursorError) Unwrap() error { return e.Err }

// IsInvalidCursorError reports whether err is or wraps an InvalidCursorError.
func IsInvalidCursorError(err error) bool {
	var ice *InvalidCursorError
	return errors.As(err, &ice)
}

// offsetCursor is the decoded shape of the OffsetPagination cursor.
type offsetCursor struct {
	Offset int64 `json:"offset"`
}

// OffsetPagination returns a (middleware, processor) pair implementing
// offset/limit pagination. limitField and offsetField name scalar
// integer fields on the gRPC request (int32/int64/uint32/uint64). The
// MCP cursor is a base64-encoded {"offset": N} JSON object. pageSize
// must be positive. Register both halves together.
func OffsetPagination(limitField, offsetField string, pageSize int) (ResourceListMiddleware, ResourceListResultProcessor) {
	if pageSize <= 0 {
		panic(fmt.Sprintf("protomcp: OffsetPagination: pageSize must be positive, got %d", pageSize))
	}
	limit := int64(pageSize)

	mw := func(next ResourceListHandler) ResourceListHandler {
		return func(ctx context.Context, req *mcp.ListResourcesRequest, g *GRPCData) (*mcp.ListResourcesResult, error) {
			off, err := DecodeOffsetCursor(ListCursor(req))
			if err != nil {
				return nil, &InvalidCursorError{Err: err}
			}
			if err := SetProtoIntField(g.Input, limitField, limit); err != nil {
				return nil, err
			}
			if err := SetProtoIntField(g.Input, offsetField, off); err != nil {
				return nil, err
			}
			return next(ctx, req, g)
		}
	}

	proc := func(_ context.Context, _ *GRPCData, data *MCPData[*mcp.ListResourcesRequest, *mcp.ListResourcesResult]) (*mcp.ListResourcesResult, error) {
		result := data.Output
		if result == nil {
			return nil, nil
		}
		// A partial page is terminal.
		if int64(len(result.Resources)) < limit {
			return result, nil
		}
		off, err := DecodeOffsetCursor(ListCursor(data.Input))
		if err != nil {
			off = 0
		}
		nextOff := off + int64(len(result.Resources))
		enc, err := EncodeOffsetCursor(nextOff)
		if err != nil {
			return nil, err
		}
		result.NextCursor = enc
		return result, nil
	}

	return mw, proc
}

// PageTokenPagination returns a (middleware, processor) pair
// implementing AIP-158 page_token pagination. pageTokenField and
// nextPageTokenField must be string-typed on the request and response.
// pageSizeField may be empty when the backend takes no size. Register
// both halves together.
func PageTokenPagination(pageTokenField, nextPageTokenField, pageSizeField string, pageSize int) (ResourceListMiddleware, ResourceListResultProcessor) {
	if pageSize < 0 {
		panic(fmt.Sprintf("protomcp: PageTokenPagination: pageSize must be non-negative, got %d", pageSize))
	}
	if nextPageTokenField == "" {
		panic("protomcp: PageTokenPagination: nextPageTokenField must be non-empty")
	}
	size := int64(pageSize)

	mw := func(next ResourceListHandler) ResourceListHandler {
		return func(ctx context.Context, req *mcp.ListResourcesRequest, g *GRPCData) (*mcp.ListResourcesResult, error) {
			if err := SetProtoStringField(g.Input, pageTokenField, ListCursor(req)); err != nil {
				return nil, err
			}
			if pageSizeField != "" && size > 0 {
				if err := SetProtoIntField(g.Input, pageSizeField, size); err != nil {
					return nil, err
				}
			}
			return next(ctx, req, g)
		}
	}

	proc := func(_ context.Context, g *GRPCData, data *MCPData[*mcp.ListResourcesRequest, *mcp.ListResourcesResult]) (*mcp.ListResourcesResult, error) {
		result := data.Output
		if result == nil || g == nil || g.Output == nil {
			return result, nil
		}
		tok, err := GetProtoStringField(g.Output, nextPageTokenField)
		if err != nil {
			return nil, fmt.Errorf("protomcp: PageTokenPagination: read %q: %w", nextPageTokenField, err)
		}
		if tok != "" {
			result.NextCursor = tok
		}
		return result, nil
	}

	return mw, proc
}

// ListCursor returns the request cursor, tolerating a nil Params.
func ListCursor(req *mcp.ListResourcesRequest) string {
	if req == nil || req.Params == nil {
		return ""
	}
	return req.Params.Cursor
}

// EncodeOffsetCursor renders offset as base64(JSON({"offset": N})),
// the codec OffsetPagination uses.
func EncodeOffsetCursor(offset int64) (string, error) {
	raw, err := json.Marshal(offsetCursor{Offset: offset})
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(raw), nil
}

// DecodeOffsetCursor parses a cursor from EncodeOffsetCursor. An empty
// string decodes to 0.
func DecodeOffsetCursor(cursor string) (int64, error) {
	if cursor == "" {
		return 0, nil
	}
	raw, err := base64.StdEncoding.DecodeString(cursor)
	if err != nil {
		return 0, fmt.Errorf("cursor not base64: %w", err)
	}
	var c offsetCursor
	if err := json.Unmarshal(raw, &c); err != nil {
		return 0, fmt.Errorf("cursor not JSON: %w", err)
	}
	if c.Offset < 0 {
		return 0, fmt.Errorf("cursor offset is negative: %d", c.Offset)
	}
	return c.Offset, nil
}

// SetProtoIntField sets the integer field named name on msg. Accepts
// int32/int64/uint32/uint64; unsigned kinds clamp negatives to 0.
// Accepts either the proto name or the JSONName.
func SetProtoIntField(msg protoreflect.ProtoMessage, name string, v int64) error {
	if msg == nil {
		return fmt.Errorf("protomcp: SetProtoIntField: nil message")
	}
	m := msg.ProtoReflect()
	fd := m.Descriptor().Fields().ByName(protoreflect.Name(name))
	if fd == nil {
		fd = m.Descriptor().Fields().ByJSONName(name)
	}
	if fd == nil {
		return fmt.Errorf("protomcp: field %q not found on %s", name, m.Descriptor().FullName())
	}
	switch fd.Kind() {
	case protoreflect.Int32Kind, protoreflect.Sint32Kind, protoreflect.Sfixed32Kind:
		if v > math.MaxInt32 {
			v = math.MaxInt32
		} else if v < math.MinInt32 {
			v = math.MinInt32
		}
		m.Set(fd, protoreflect.ValueOfInt32(int32(v)))
	case protoreflect.Int64Kind, protoreflect.Sint64Kind, protoreflect.Sfixed64Kind:
		m.Set(fd, protoreflect.ValueOfInt64(v))
	case protoreflect.Uint32Kind, protoreflect.Fixed32Kind:
		if v < 0 {
			v = 0
		} else if v > math.MaxUint32 {
			v = math.MaxUint32
		}
		m.Set(fd, protoreflect.ValueOfUint32(uint32(v)))
	case protoreflect.Uint64Kind, protoreflect.Fixed64Kind:
		if v < 0 {
			v = 0
		}
		m.Set(fd, protoreflect.ValueOfUint64(uint64(v)))
	default:
		return fmt.Errorf("protomcp: field %q on %s is not an integer (kind=%s)",
			name, m.Descriptor().FullName(), fd.Kind())
	}
	return nil
}

// SetProtoStringField sets the string field named name on msg. Accepts
// either the proto name or the JSONName.
func SetProtoStringField(msg protoreflect.ProtoMessage, name, v string) error {
	if msg == nil {
		return fmt.Errorf("protomcp: SetProtoStringField: nil message")
	}
	m := msg.ProtoReflect()
	fd := m.Descriptor().Fields().ByName(protoreflect.Name(name))
	if fd == nil {
		fd = m.Descriptor().Fields().ByJSONName(name)
	}
	if fd == nil {
		return fmt.Errorf("protomcp: field %q not found on %s", name, m.Descriptor().FullName())
	}
	if fd.Kind() != protoreflect.StringKind {
		return fmt.Errorf("protomcp: field %q on %s is not a string (kind=%s)",
			name, m.Descriptor().FullName(), fd.Kind())
	}
	m.Set(fd, protoreflect.ValueOfString(v))
	return nil
}

// GetProtoStringField reads the string field named name from msg.
// Accepts either the proto name or the JSONName.
func GetProtoStringField(msg protoreflect.ProtoMessage, name string) (string, error) {
	if msg == nil {
		return "", fmt.Errorf("protomcp: GetProtoStringField: nil message")
	}
	m := msg.ProtoReflect()
	fd := m.Descriptor().Fields().ByName(protoreflect.Name(name))
	if fd == nil {
		fd = m.Descriptor().Fields().ByJSONName(name)
	}
	if fd == nil {
		return "", fmt.Errorf("protomcp: field %q not found on %s", name, m.Descriptor().FullName())
	}
	if fd.Kind() != protoreflect.StringKind {
		return "", fmt.Errorf("protomcp: field %q on %s is not a string (kind=%s)",
			name, m.Descriptor().FullName(), fd.Kind())
	}
	return m.Get(fd).String(), nil
}
