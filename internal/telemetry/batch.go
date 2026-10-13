package telemetry

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// Batcher combines the resource objects in internally encoded OTLP bodies while keeping
// each emitted request below MaxBodyBytes. Add is synchronous: an emit error stops the
// batcher and is returned by every subsequent operation.
type Batcher struct {
	emit    func(Body) error
	pending map[string]*pendingBody
	err     error
}

type pendingBody struct {
	path   string
	header []byte
	data   bytes.Buffer
}

// NewBatcher returns a batcher whose callback receives accepted OTLP request bodies.
func NewBatcher(emit func(Body) error) *Batcher {
	return &Batcher{emit: emit, pending: map[string]*pendingBody{}}
}

// Add accepts one body produced by Encode. It accepts only the exact internal logs and
// metrics envelopes, preserving each envelope's resource object bytes and order.
func (b *Batcher) Add(body Body) error {
	if b.err != nil {
		return b.err
	}
	resources, key, err := decodeResources(body)
	if err != nil {
		return err
	}
	if len(resources) == 0 {
		return nil
	}
	for _, raw := range resources {
		if len(header(key))+len(raw)+len(footer()) > MaxBodyBytes {
			return fmt.Errorf("telemetry: %s resource exceeds %d bytes", body.Path, MaxBodyBytes)
		}
	}

	p := b.pending[body.Path]
	if p == nil {
		p = newPending(body.Path, key)
		b.pending[body.Path] = p
	}
	for _, raw := range resources {
		comma := 0
		if p.data.Len() > len(p.header) {
			comma = 1
		}
		if p.data.Len()+comma+len(raw)+len(footer()) > MaxBodyBytes {
			if err := b.flushPath(body.Path); err != nil {
				return err
			}
			p = newPending(body.Path, key)
			b.pending[body.Path] = p
			comma = 0
		}
		if comma != 0 {
			p.data.WriteByte(',')
		}
		p.data.Write(raw)
	}
	return nil
}

// Flush emits pending logs first, then pending metrics, for deterministic output order.
func (b *Batcher) Flush() error {
	if b.err != nil {
		return b.err
	}
	if err := b.flushPath("/v1/logs"); err != nil {
		return err
	}
	return b.flushPath("/v1/metrics")
}

func (b *Batcher) flushPath(path string) error {
	p := b.pending[path]
	if p == nil || p.data.Len() == len(p.header) {
		return nil
	}
	data := append(p.data.Bytes(), ']', '}')
	if err := b.emitBody(Body{Path: p.path, Data: data}); err != nil {
		return err
	}
	delete(b.pending, path)
	return nil
}

func (b *Batcher) emitBody(body Body) error {
	if err := b.emit(body); err != nil {
		b.err = err
		return err
	}
	return nil
}

func newPending(path, key string) *pendingBody {
	h := header(key)
	var data bytes.Buffer
	data.Write(h)
	return &pendingBody{path: path, header: h, data: data}
}

func header(key string) []byte { return []byte(`{"` + key + `":[`) }

func footer() []byte { return []byte(`]}`) }

func decodeResources(body Body) ([]json.RawMessage, string, error) {
	key := ""
	switch body.Path {
	case "/v1/logs":
		key = "resourceLogs"
	case "/v1/metrics":
		key = "resourceMetrics"
	default:
		return nil, "", fmt.Errorf("telemetry: unsupported OTLP path %q", body.Path)
	}
	if len(body.Data) > MaxBodyBytes {
		return nil, "", fmt.Errorf("telemetry: %s body exceeds %d bytes", body.Path, MaxBodyBytes)
	}
	var top map[string][]json.RawMessage
	if err := json.Unmarshal(body.Data, &top); err != nil {
		return nil, "", fmt.Errorf("telemetry: invalid %s body: %w", body.Path, err)
	}
	if len(top) != 1 {
		return nil, "", fmt.Errorf("telemetry: invalid %s envelope", body.Path)
	}
	resources, ok := top[key]
	if !ok {
		return nil, "", fmt.Errorf("telemetry: %s envelope lacks %s", body.Path, key)
	}
	for _, resource := range resources {
		trimmed := bytes.TrimSpace(resource)
		if len(trimmed) == 0 || trimmed[0] != '{' {
			return nil, "", fmt.Errorf("telemetry: %s resource is not an object", body.Path)
		}
	}
	return resources, key, nil
}
