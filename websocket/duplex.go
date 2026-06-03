package websocket

import (
	"context"
	"errors"
	"sync"

	"github.com/ethereum/go-ethereum/log"
)

// pipe is a bidirectional bridge between two connections.  It is meant to
// help facilitate the bridging of messages between two connections, such
// case an upstream and dosntream connection.
type pipe struct {
	upstream, downstream Conn
}

// Close will close the connections of the pipe.  It will attempt to close
// both connections and return any errors encountered during the process.
func (p *pipe) Close() error {
	// Let's close the connections
	return errors.Join(p.downstream.Close(1001, "closing downstream"),
		p.upstream.Close(1000, "closing upstream"),
	)
}

// bridgeDownstream reads messages from the downstream
// connection and writes them to the upstream connection.
func (p *pipe) bridgeDownstream(ctx context.Context) error {
	for {
		select {
		default:
		case <-ctx.Done():
			// We were told to cancel. No need to inspect the error, just exit.
			return nil
		}

		mesageType, message, err := p.downstream.Read(ctx)
		if err != nil {
			return err
		}

		if err := p.upstream.Write(ctx, mesageType, message); err != nil {
			return err
		}
	}
}

// bridgeUpstream reads messages from the upstream connection and writes them
// to the downstream connection.
func (p *pipe) bridgeUpstream(ctx context.Context) error {
	for {
		select {
		default:
		case <-ctx.Done():
			// We were told to cancel. No need to inspect the error, just exit.
			return nil
		}

		messageType, message, err := p.upstream.Read(ctx)
		if err != nil {
			return err
		}

		if err := p.downstream.Write(ctx, messageType, message); err != nil {
			return err
		}
	}
}

// Bridge starts the bidirectional bridging of messages between the upstream
// and and downstream connections.  It will continue to bridge messages until
// either
func (p *pipe) Bridge(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var wg sync.WaitGroup

	ch := make(chan error, 2)
	wg.Add(1)
	go (func(p *pipe, wg *sync.WaitGroup, ch chan<- error) {
		defer cancel()
		defer wg.Done()
		ch <- p.bridgeDownstream(ctx)
	})(p, &wg, ch)

	wg.Add(1)
	go (func(p *pipe, wg *sync.WaitGroup, ch chan<- error) {
		defer cancel()
		defer wg.Done()
		ch <- p.bridgeUpstream(ctx)
	})(p, &wg, ch)

	wg.Wait()
	close(ch)

	var errs []error
	for err := range ch {
		errs = append(errs, err)
	}

	return errors.Join(errs...)
}

// Bridge is a helper function that creates a new pipe and starts the bridging
// of messages between the upstream and downstream connections.  It will
// continue to bridge messages until either connection is closed or an error
// is encountered.
func Bridge(ctx context.Context, upstream, downstream Conn) (err error) {
	p := &pipe{
		upstream:   upstream,
		downstream: downstream,
	}

	defer (func(p *pipe) {
		if err := p.Close(); err != nil {
			// Ensures that the connections are closed and severed.
			log.Error("failed to close pipe connections", "error", err)
		}
	})(p)

	return p.Bridge(ctx)
}
