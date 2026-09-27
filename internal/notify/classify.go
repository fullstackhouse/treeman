package notify

import (
	"strings"

	"github.com/stubbedev/treeman/internal/store"
)

// The four `treeman status` buckets a worktree can fall into. Kept in
// sync with the constants in cmd/treeman/cmd/status.go — these are the
// user-facing names the `notifications.events:` config lists.
const (
	BucketStable = "stable"
	BucketUp     = "up"
	BucketDown   = "down"
	BucketFailed = "failed"
)

// Bucket maps a store event (its type + level) to the status bucket a
// notification would announce, or "" when the event is not a lifecycle
// status transition worth notifying on.
//
// Mirrors deriveStatusBucket in cmd/treeman/cmd/status.go, but operates
// per-event (the moment of transition) rather than by replaying the
// newest event for a worktree:
//
//   - worktree:create:start             → up     (preparing began)
//   - worktree:create:end               → stable (ready)
//   - worktree:create:error (lvl=error) → failed (finalize errored)
//   - worktree:delete:start             → down   (teardown began)
//   - worktree:reap:start               → down
//
// The terminal teardown events (worktree:delete:end /
// worktree:reap:end) are intentionally unmapped: the worktree is gone,
// so "down" already fired at teardown start and a second banner for
// the completion would just be noise.
func Bucket(eventType, level string) string {
	switch eventType {
	case store.EvtWorktreeCreateStart:
		return BucketUp
	case store.EvtWorktreeCreateEnd:
		return BucketStable
	case store.EvtWorktreeCreateError:
		// Only ever written as the terminal error event (see
		// finalize.go / stale_finalize.go); gate on level so a future
		// non-error use can't masquerade as a failure.
		if level == store.LevelError {
			return BucketFailed
		}
		return ""
	case store.EvtWorktreeDeleteStart, store.EvtWorktreeReapStart:
		return BucketDown
	default:
		return ""
	}
}

// urgencyForBucket maps a bucket to its notification urgency. Only a
// failed finalize is critical; everything else is informational.
func urgencyForBucket(bucket string) Urgency {
	if bucket == BucketFailed {
		return UrgencyCritical
	}
	return UrgencyNormal
}

// maxBodyLen caps composed bodies: notify-send and osascript both
// truncate long text ungracefully, so the error snippet is fitted
// into whatever room the fixed prefix + follow-up hint leave.
const maxBodyLen = 200

// Compose builds the notification banner for a status transition.
// `repo` is the repository's display name, `target` the worktree
// branch or slug (either may be empty, in which case it's omitted),
// and `detail` the triggering event's message — only the failed
// bucket surfaces it (first line, capped) and appends the follow-up
// command for that worktree.
func Compose(bucket, repo, target, detail string) Notification {
	subject := repo
	if target != "" {
		if subject != "" {
			subject += " · " + target
		} else {
			subject = target
		}
	}
	if subject == "" {
		subject = "worktree"
	}

	var title, body string
	switch bucket {
	case BucketStable:
		title = "treeman: ready"
		body = subject + " finished preparing"
	case BucketUp:
		title = "treeman: preparing"
		body = subject + " is being prepared"
	case BucketDown:
		title = "treeman: tearing down"
		body = subject + " is being torn down"
	case BucketFailed:
		title = "treeman: failed"
		base := subject + " failed to prepare"
		hint := ""
		if target != "" {
			hint = " — run: treeman worktree show " + target
		}
		body = base + hint
		if first := firstLine(detail); first != "" {
			// Reserve room for the hint so the follow-up command always
			// survives the cap instead of being cut off by it.
			budget := maxBodyLen - len([]rune(base)) - len(": ") - len([]rune(hint))
			if budget > 0 {
				body = base + ": " + ellipsize(first, budget) + hint
			}
		}
	default:
		title = "treeman"
		body = subject
	}
	return Notification{Title: title, Body: body, Urgency: urgencyForBucket(bucket)}
}

// firstLine returns the first non-empty line of a multi-line error.
func firstLine(s string) string {
	for line := range strings.SplitSeq(s, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			return line
		}
	}
	return ""
}

// ellipsize truncates s to at most limit runes with a trailing ellipsis.
func ellipsize(s string, limit int) string {
	r := []rune(s)
	if len(r) <= limit {
		return s
	}
	if limit <= 1 {
		return "…"
	}
	return string(r[:limit-1]) + "…"
}
