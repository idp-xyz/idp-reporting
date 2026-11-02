package reporting

import "sync"

// queryGate counts, per identity, the reads the service is running (S04-G, docs/S04G_SCOPE.md G3): POST /queries and the row reads of a
// snapshot. It is per process, in memory: it protects this process's cores and its connections to the source product, and the durable
// quotas of S04-D stay the cross-process ones. An identity with nothing running has no entry, so the map is as large as the identities
// running something and never grows with the identities seen.
type queryGate struct {
	mu      sync.Mutex
	running map[Identity]int
}

// enter takes a slot of the identity, or says there is none under limit; a refusal changes nothing.
func (g *queryGate) enter(i Identity, limit int) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.running[i] >= limit {
		return false
	}
	if g.running == nil {
		g.running = map[Identity]int{}
	}
	g.running[i]++
	return true
}

// leave gives the slot back; it is called once for each successful enter, on every way out of the read (deferred).
func (g *queryGate) leave(i Identity) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.running[i] <= 1 {
		delete(g.running, i)
		return
	}
	g.running[i]--
}

// admitRead takes a slot for a read of the principal's, or returns the quota error and a nil release. An identity that is not valid takes no
// slot (the read is refused by what it calls, as before), so the map never holds a made-up identity.
func (s *Service) admitRead(p Principal) (release func(), err error) {
	if !p.Identity.Valid() {
		return func() {}, nil
	}
	if !s.gate.enter(p.Identity, s.quotas.QueriesInFlight) {
		return nil, &QuotaError{Quota: QuotaQueriesInFlight}
	}
	return func() { s.gate.leave(p.Identity) }, nil
}

// QueryGateSizeForTests is the number of identities the gate holds a slot for, for the test that it keeps none for an identity that is done.
func QueryGateSizeForTests(s *Service) int {
	s.gate.mu.Lock()
	defer s.gate.mu.Unlock()
	return len(s.gate.running)
}
