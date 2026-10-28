package reporting

import (
	"testing"
	"time"
)

// The time a worker has. A void is bounded to the 5 seconds the scope allows (docs/S03H_SCOPE.md), and a
// failure is recorded within the lease: after the work (WorkTimeout), the read that confirms a lost lease
// (leaseCheckTimeout) and the write that records the failure (failTimeout, 2 s) the claim's lease has not
// run out, which is what the fence on Fail needs to take. The void follows the recorded failure and has the
// job to itself, so it is outside this budget.
func TestFailureIsRecordedWithinTheLease(t *testing.T) {
	if VoidTimeout != 5*time.Second {
		t.Errorf("VoidTimeout is %v; the scope says at most 5 s", VoidTimeout)
	}
	if failTimeout != 2*time.Second {
		t.Errorf("failTimeout is %v; the budget below is reasoned with 2 s", failTimeout)
	}
	if WorkTimeout+leaseCheckTimeout+2*time.Second >= LeaseDuration {
		t.Errorf("WorkTimeout %v + leaseCheckTimeout %v + 2 s is not within the lease of %v: a worker that used all its time could no longer record its failure", WorkTimeout, leaseCheckTimeout, LeaseDuration)
	}
	if WorkTimeout+leaseCheckTimeout+failTimeout >= LeaseDuration {
		t.Errorf("the failure of a worker that used all its time is recorded after its lease ran out")
	}
}
