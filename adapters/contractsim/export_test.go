package contractsim

// HeldForTests reports what the simulator remembers of the reads it prepared: how many reads, and how many of them still hold the data
// version they froze. It exists for the test of the simulator's lifetimes (ADR 0026); nothing outside the package's tests can see it.
func HeldForTests(s *Simulator) (reads, versions int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, f := range s.frozen {
		reads++
		if f.records != nil {
			versions++
		}
	}
	return reads, versions
}
