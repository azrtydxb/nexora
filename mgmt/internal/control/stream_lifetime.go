package control

import (
	"context"
	"sync"

	controlv1 "github.com/piwi3910/nexora/gen/go/nexora/control/v1"
)

// There is at most one transport reader and one sender per RPC. Their IO uses
// the RPC context, so Connect must return before joining them is possible.
// Application receive processing, UPDATE callbacks and TLS retry are joined before cleanup.
type streamWorkers struct {
	sendDone, recvDone, receiveDone, retryDone chan struct{}
}

func newStreamWorkers() *streamWorkers {
	return &streamWorkers{make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{})}
}

type receiveResult struct {
	message *controlv1.EngineMessage
	err     error
}

type cancelableReceive struct {
	controlv1.EngineControl_ConnectServer
	ctx     context.Context
	request chan struct{}
	result  chan receiveResult
}

func newCancelableReceive(ctx context.Context, stream controlv1.EngineControl_ConnectServer, done chan struct{}) *cancelableReceive {
	r := &cancelableReceive{EngineControl_ConnectServer: stream, ctx: ctx, request: make(chan struct{}), result: make(chan receiveResult)}
	go func() {
		defer close(done)
		for {
			// Demand-driven: do not read another message while the previous one
			// is being processed, and never spawn a goroutine for each message.
			select {
			case <-ctx.Done():
				return
			case <-r.request:
			}
			m, err := stream.Recv()
			select {
			case <-ctx.Done():
				return
			case r.result <- receiveResult{m, err}:
			}
			if err != nil {
				return
			}
		}
	}()
	return r
}

func (r *cancelableReceive) Recv() (*controlv1.EngineMessage, error) {
	select {
	case <-r.ctx.Done():
		return nil, r.ctx.Err()
	case r.request <- struct{}{}:
	}
	select {
	case <-r.ctx.Done():
		return nil, r.ctx.Err()
	case result := <-r.result:
		return result.message, result.err
	}
}

// Admission and Add share the stop mutex, so Wait cannot race a new worker.
// Only bounded application callbacks belong here; transport IO needs RPC return.
type applicationWorkers struct {
	mu      sync.Mutex
	stopped bool
	wg      sync.WaitGroup
}

func (w *applicationWorkers) start() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.stopped {
		return false
	}
	w.wg.Add(1)
	return true
}
func (w *applicationWorkers) stop() {
	w.mu.Lock()
	w.stopped = true
	w.mu.Unlock()
}
func (w *applicationWorkers) wait() { w.wg.Wait() }
