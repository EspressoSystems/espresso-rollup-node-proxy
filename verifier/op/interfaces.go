package verifier

import "github.com/ethereum-optimism/optimism/op-service/eth"

// opFinalitySnapshot is the FinalitySnapshot the finality poller caches for the
// OP verifier. It carries the full SyncStatus so Refresh can reuse it without a
// separate RPC call.
type OpFinalitySnapshot struct {
	syncStatus *eth.SyncStatus
}

func (s OpFinalitySnapshot) FinalizedL2() uint64 { return s.syncStatus.FinalizedL2.Number }
