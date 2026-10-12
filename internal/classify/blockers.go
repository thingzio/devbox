package classify

import (
	"fmt"
	"strings"
)

type cause struct {
	cause  string
	action string
}

var (
	passingChecks    = map[string]bool{"SUCCESS": true, "NEUTRAL": true, "SKIPPED": true}
	rerunnableChecks = map[string]bool{"FAILURE": true, "TIMED_OUT": true, "CANCELLED": true, "STARTUP_FAILURE": true}
	eligibleStates   = map[string]bool{"CLEAN": true, "HAS_HOOKS": true, "UNSTABLE": true}
)

// mergeBlockers names why GitHub would refuse the merge (§5.4); empty means mergeable.
func mergeBlockers(rec Record) []cause {
	var causes []cause
	switch state := rec.MergeState; {
	case state == "DIRTY":
		causes = append(causes, cause{"conflicts with base", "ask the author to rebase"})
	case state == "BEHIND":
		action := "update the branch"
		if rec.AutoMerge {
			action = "run serial-auto-merge"
		}
		causes = append(causes, cause{"behind base", action})
	case state == "UNKNOWN":
		causes = append(causes, cause{"mergeability unknown", "check the merge box on GitHub"})
	case state == "BLOCKED":
		for _, c := range rec.Checks {
			if !c.Required || passingChecks[c.State] {
				continue
			}
			action := "wait for the check, or investigate why it has not passed"
			if c.RunID != 0 && rerunnableChecks[c.State] {
				action = fmt.Sprintf("`gh run rerun %d --failed` after a flake check", c.RunID)
			}
			causes = append(causes, cause{fmt.Sprintf("required check `%s` is %s", c.Name, strings.ToLower(c.State)), action})
		}
	case !eligibleStates[state]:
		causes = append(causes, cause{"merge state " + strings.ToLower(state), "check the merge box on GitHub"})
	}
	if c := signatureBlocker(rec); c != nil {
		causes = append(causes, *c)
	}
	if rec.MergeState == "BLOCKED" && len(causes) == 0 {
		causes = append(causes, cause{
			fmt.Sprintf("blocked by branch policy; cause not identified (%d unresolved threads)", rec.UnresolvedThreads),
			"check the merge box on GitHub",
		})
	}
	return causes
}

func signatureBlocker(rec Record) *cause {
	if rec.SignaturePolicy == "absent" {
		return nil
	}
	if !rec.SignaturesComplete {
		return &cause{"signature state unknown", "check commit signatures on GitHub"}
	}
	if len(rec.UnverifiedCommits) == 0 {
		return nil
	}
	listed := make([]string, len(rec.UnverifiedCommits))
	for i, u := range rec.UnverifiedCommits {
		listed[i] = fmt.Sprintf("%s (%s)", short(u.SHA), u.Reason)
	}
	joined := strings.Join(listed, ", ")
	if rec.SignaturePolicy == "required" {
		return &cause{"unverified commits: " + joined, "ask the author to sign the listed commits"}
	}
	return &cause{
		fmt.Sprintf("%d unverified commits; signature policy unreadable: %s", len(rec.UnverifiedCommits), joined),
		"confirm the signature policy, then ask the author to sign if required",
	}
}

func summarize(causes []cause) (string, string) {
	reasons := make([]string, 0, len(causes))
	var actions []string
	seen := map[string]bool{}
	for _, c := range causes {
		reasons = append(reasons, c.cause)
		if !seen[c.action] {
			seen[c.action] = true
			actions = append(actions, c.action)
		}
	}
	return "approved; " + strings.Join(reasons, "; "), strings.Join(actions, "; ")
}

func short(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}
