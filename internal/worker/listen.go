package worker

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/vipul2902/clouddeploy/internal/deployments"
)

// listen holds a dedicated connection on LISTEN and wakes an idle loop for
// every notification. Notifications are only a latency optimization: if the
// connection drops, loops still find work via PollInterval, and listen
// reconnects with backoff.
func (w *Worker) listen(ctx context.Context) {
	if w.pool == nil {
		return
	}
	backoff := time.Second
	for ctx.Err() == nil {
		err := w.listenOnce(ctx)
		if ctx.Err() != nil {
			return
		}
		w.logger.Warn("queue listener disconnected; polling until reconnected", "err", err, "retry_in", backoff)
		sleep(ctx, backoff)
		backoff = min(backoff*2, 30*time.Second)
	}
}

func (w *Worker) listenOnce(ctx context.Context) error {
	conn, err := w.pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer func() {
		// Return the connection clean: a pooled connection must not keep
		// listening on behalf of whoever borrows it next.
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
		defer cancel()
		if _, err := conn.Exec(cleanup, "UNLISTEN *"); err != nil {
			conn.Conn().Close(cleanup) // broken: let the pool discard it
		}
		conn.Release()
	}()
	if _, err := conn.Exec(ctx, "LISTEN "+pgx.Identifier{deployments.QueueChannel}.Sanitize()); err != nil {
		return err
	}
	// Anything queued while we were disconnected is found by this wake-up.
	w.signal()
	for {
		if _, err := conn.Conn().WaitForNotification(ctx); err != nil {
			return err
		}
		w.signal()
	}
}
