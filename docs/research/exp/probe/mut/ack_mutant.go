//go:build mutant_noack

package mut

func PersistAck() bool { return false }
