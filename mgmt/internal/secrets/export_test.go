//go:build cgo

package secrets

// KillHSMSessionsForTest closes every session of the token underneath the pool, as a token reset does.
func (b *Box) KillHSMSessionsForTest() error { return b.hsm.ctx.CloseAllSessions(b.hsm.slot) }

// HSMPoolLenForTest reports the sessions currently idle in the pool.
func (b *Box) HSMPoolLenForTest() int { return len(b.hsm.pool) }
