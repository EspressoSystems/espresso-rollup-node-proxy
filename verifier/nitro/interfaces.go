package verifier

// NitroFinalitySnapshot is the LatestSnapshot the finality poller caches for the
// Nitro verifier. Nitro only needs the finalized L2 block number, so the
// snapshot is just that number.
type NitroFinalitySnapshot uint64

func (s NitroFinalitySnapshot) FinalizedL2() uint64 { return uint64(s) }
