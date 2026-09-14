package client

import "context"

// SetMessageRead changes one message's read state when the transport supports
// message-level updates. A false result means the provider is left unchanged;
// notably OpenMail exposes only thread-wide read state.
func SetMessageRead(ctx context.Context, transport Transport, id string, read bool) (bool, error) {
	if !supportsMessageRead(transport) {
		return false, nil
	}
	if setter, ok := transport.(interface {
		SetMessageRead(context.Context, string, bool) (bool, error)
	}); ok {
		return setter.SetMessageRead(ctx, id, read)
	}
	return false, nil
}

func (endpoint *pairEndpoint) SetMessageRead(ctx context.Context, id string, read bool) (bool, error) {
	// Resolve through the endpoint before mutating shared provider state, so an
	// ID belonging to another owner cannot be acknowledged or re-queued.
	if err := endpoint.ensureMessage(ctx, id); err != nil {
		return false, err
	}
	return SetMessageRead(ctx, endpoint.router.raw, id, read)
}

func (transport *retryTransport) SetMessageRead(ctx context.Context, id string, read bool) (supported bool, err error) {
	err = transport.retry(ctx, "message read state", func() error {
		supported, err = SetMessageRead(ctx, transport.Transport, id, read)
		return err
	})
	return
}

func supportsMessageRead(transport Transport) bool {
	switch t := transport.(type) {
	case *pairEndpoint:
		return supportsMessageRead(t.router.raw)
	case *retryTransport:
		return supportsMessageRead(t.Transport)
	default:
		_, ok := transport.(interface {
			SetMessageRead(context.Context, string, bool) (bool, error)
		})
		return ok
	}
}
