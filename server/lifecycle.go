package server

import (
	"context"
	"errors"
	"net/http"
	"time"

	r "idp.local/reporting/reporting"
)

// codeOf is the code a worker failure is logged by: the platform's own codes, else EXECUTION_FAILED. The error itself is never logged.
func codeOf(err error) string {
	for _, known := range []error{r.ErrDenied, r.ErrUnavailable, r.ErrInvalid, r.ErrIntegrity, r.ErrBudget, r.ErrLease, r.ErrUnsupported, r.ErrNotFound, r.ErrUnpublished, r.ErrConflict, r.ErrUnreconciled} {
		if errors.Is(err, known) {
			return known.Error()
		}
	}
	return "EXECUTION_FAILED"
}

// startWorkers starts the worker loops and, when the configuration asks for one, the sweep loop.
func (s *Server) startWorkers() {
	s.loopCtx, s.loopCancel = context.WithCancel(context.Background())
	s.workCtx, s.workCancel = context.WithCancel(context.Background())
	for i := 0; i < s.cfg.WorkerCount(); i++ {
		s.workers.Add(1)
		go s.worker()
	}
	if s.cfg.Retention.SweepInterval > 0 {
		s.workers.Add(1)
		go s.sweepLoop()
	}
}

// worker claims and runs jobs until the drain begins. A job it holds runs to its end on the work context.
func (s *Server) worker() {
	defer s.workers.Done()
	timer := time.NewTimer(s.opts.poll)
	defer timer.Stop()
	for s.loopCtx.Err() == nil {
		s.busy.Add(1)
		ran, err := s.svc.RunOne(s.workCtx)
		s.busy.Add(-1)
		if err != nil && !errors.Is(err, context.Canceled) {
			s.log.warn("worker_failed", map[string]any{"code": codeOf(err)})
			s.metrics.workerFailed(codeOf(err))
		}
		if ran && err == nil {
			s.metrics.jobRan()
		}
		if ran && err == nil {
			continue
		}
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		timer.Reset(s.opts.poll)
		select {
		case <-s.loopCtx.Done():
			return
		case <-timer.C:
		}
	}
}

// sweepLoop runs the retention sweep every retention.sweep_interval seconds and logs its counts, or the code of its failure. The store the
// production opener returns is the application role's, which never deletes (S04-A): there the sweep is refused and logged by its code, and
// the sweep is run by `reporting-migrate sweep`; the loop is for a store whose role may sweep, and is off by default.
func (s *Server) sweepLoop() {
	defer s.workers.Done()
	tick, stop := s.opts.sweepTicker(seconds(s.cfg.Retention.SweepInterval))
	defer stop()
	opts := r.SweepOptions{Grace: seconds(s.cfg.Retention.Grace), Batch: s.cfg.Retention.Batch}
	for {
		select {
		case <-s.loopCtx.Done():
			return
		case <-tick:
			s.sweepOnce(opts)
			if h := s.opts.hooks; h != nil && h.Swept != nil {
				select {
				case h.Swept <- struct{}{}:
				default:
				}
			}
		}
	}
}

func (s *Server) sweepOnce(opts r.SweepOptions) {
	s.busy.Add(1)
	defer s.busy.Add(-1)
	res, err := s.svc.Sweep(s.workCtx, opts)
	fields := map[string]any{"artifacts": res.Artifacts, "snapshots": res.Snapshots, "jobs": res.Jobs, "objects": res.Objects, "orphans": res.Orphans, "objects_left": res.ObjectsLeft}
	if err != nil {
		fields["code"] = codeOf(err)
		s.log.warn("sweep_failed", fields)
		s.metrics.swept(codeOf(err))
		return
	}
	s.log.info("sweep", fields)
	s.metrics.swept("ok")
}

// serve runs the listener until ctx ends, then the drain.
func (s *Server) serve(ctx context.Context) {
	defer close(s.done)
	served := make(chan error, 1)
	go func() {
		if s.tls {
			served <- s.srv.ServeTLS(s.listener, "", "")
		} else {
			served <- s.srv.Serve(s.listener)
		}
	}()
	var serveErr error
	select {
	case <-ctx.Done():
	case serveErr = <-served:
		served <- serveErr // consumed again below
	}
	s.result = s.drain(served, serveErr)
}

// drain is the shutdown: readiness says draining at once, the loops stop claiming, the listener closes, in-flight requests and the jobs the
// workers hold finish within shutdown_grace, then the pools close. When the grace is used up the remaining connections are closed, the
// store is left open (a worker is still inside a call on it) and the result is ErrDrainTimeout: the process is about to exit, and a job a
// worker still holds keeps its lease and is claimed by another process after the lease runs out.
func (s *Server) drain(served chan error, serveErr error) error {
	s.draining.Store(true)
	grace := orDefault(s.cfg.ShutdownGrace, DefaultShutdownGrace)
	s.log.info("drain_begin", map[string]any{"in_flight": s.requests.count(), "workers_busy": s.busy.Load(), "grace_seconds": int(grace.Seconds())})
	if h := s.opts.hooks; h != nil && h.DrainBegan != nil {
		close(h.DrainBegan)
	}
	s.loopCancel()
	timer, stopTimer := s.opts.graceTimer(grace)
	over := make(chan struct{})
	finished := make(chan struct{})
	shutdownCtx, shutdownCancel := context.WithCancel(context.Background())
	watcher := make(chan struct{})
	go func() {
		defer close(watcher)
		select {
		case <-timer:
			close(over)
			shutdownCancel()
		case <-finished:
		}
	}()
	timedOut := false
	if err := s.srv.Shutdown(shutdownCtx); err != nil {
		timedOut = true
		_ = s.srv.Close()
	}
	workersDone := make(chan struct{})
	go func() { s.workers.Wait(); close(workersDone) }()
	select {
	case <-workersDone:
	default:
		select {
		case <-workersDone:
		case <-over:
			timedOut = true
		}
	}
	// Handlers of a forced close return when their contexts end; bounded, because they are not ours to wait for forever.
	stillServing := 0
	if timedOut {
		select {
		case <-s.requests.idle():
		case <-time.After(5 * time.Second):
		}
		stillServing = s.requests.count()
	}
	busy := s.busy.Load()
	close(finished)
	stopTimer()
	<-watcher
	shutdownCancel()
	<-served
	if timedOut && busy > 0 {
		// A worker is inside a call: closing the pool under it would fail its writes, and it is not ours to abandon cleanly. The process exits.
		s.log.warn("drain_end", map[string]any{"clean": false, "jobs_left": busy, "requests_left": stillServing})
		return ErrDrainTimeout
	}
	s.unwind()
	if timedOut {
		s.log.warn("drain_end", map[string]any{"clean": false, "jobs_left": 0, "requests_left": stillServing})
		return ErrDrainTimeout
	}
	s.log.info("drain_end", map[string]any{"clean": true, "jobs_left": 0, "requests_left": 0})
	if serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
		return serveErr
	}
	return nil
}
