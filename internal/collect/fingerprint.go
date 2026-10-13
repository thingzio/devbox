// Package collect reads GitHub evidence for pull requests and normalizes it
// into classify.Record.
package collect

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"
	"time"
)

// FingerprintFields are the PR fields whose change triggers re-enrichment.
type FingerprintFields struct {
	UpdatedAt      time.Time
	BaseOID        string
	HeadOID        string
	MergeState     string
	ReviewDecision string
	Draft          bool
	Rollup         string
}

// Fingerprint is SHA-256 over updatedAt|baseRefOid|headRefOid|mergeStateStatus|reviewDecision|isDraft|rollupState.
func Fingerprint(f FingerprintFields) string {
	s := strings.Join([]string{
		f.UpdatedAt.UTC().Format(time.RFC3339Nano), f.BaseOID, f.HeadOID, f.MergeState, f.ReviewDecision,
		strconv.FormatBool(f.Draft), f.Rollup,
	}, "|")
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}
