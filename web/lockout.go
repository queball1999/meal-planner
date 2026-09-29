package web

import (
	"context"
	"fmt"
	"log"
	"math"
	"net/http"
	"strings"
	"time"

	"goeat/middleware"
)

// ── Sign-in rate limit and lockout (QSS security design §2.1, §2.2) ────────
//
// Two layers, both checked before any bcrypt work:
//
//   - loginLimiter (in memory, per IP): loginRateMax attempts a minute from
//     one address, whatever the username. Cheap and transient.
//   - Lockout (login_attempts table, survives restarts), keyed by the
//     submitted username string so a username that doesn't exist locks
//     exactly like one that does:
//   - (username, IP): pairMax failures within lockWindow lock that pair
//     for lockFor. Stops one source guessing one account, without locking
//     the real user out on their own device.
//   - IP alone: ipMax failures across any usernames lock that IP
//     (password spraying).
//   - username alone, from many IPs: no hard lock - that would let anyone
//     lock anyone out. Each attempt is slowed instead, capped at maxDelay.
const (
	loginRateMax = 10
	pairMax      = 5
	ipMax        = 20
	lockWindow   = 5 * time.Minute
	lockFor      = 10 * time.Minute
	maxDelay     = 3 * time.Second
)

// normUsername is the lockout key for a submitted username.
func normUsername(u string) string { return strings.ToLower(strings.TrimSpace(u)) }

// lockedUntil: times are failures, newest first. If the newest max of them
// fall within lockWindow, the key is locked until lockFor after the newest.
// Attempts made while locked are refused before they're recorded, so the
// newest failure is always the one that tripped the lock.
func lockedUntil(times []time.Time, max int) time.Time {
	if len(times) < max || times[0].Sub(times[max-1]) > lockWindow {
		return time.Time{}
	}
	return times[0].Add(lockFor)
}

// loginLockRemaining reports how long the (username, IP) pair or the IP is
// still locked for, or 0. A database error fails open: sign-in itself needs
// the same database and will fail on its own.
func (s *Server) loginLockRemaining(ctx context.Context, username, ip string, now time.Time) time.Duration {
	since := now.Add(-(lockWindow + lockFor))
	var until time.Time
	if pair, err := s.store.FailedLoginTimes(ctx, username, ip, since); err == nil {
		until = lockedUntil(pair, pairMax)
	} else {
		log.Printf("lockout: pair lookup: %v", err)
	}
	if byIP, err := s.store.FailedLoginTimes(ctx, "", ip, since); err == nil {
		if u := lockedUntil(byIP, ipMax); u.After(until) {
			until = u
		}
	} else {
		log.Printf("lockout: ip lookup: %v", err)
	}
	if until.After(now) {
		return until.Sub(now)
	}
	return 0
}

// loginDelay slows attempts on a username that is failing from many places:
// half a second per failure past pairMax in the lockout window, up to
// maxDelay. The owner can still sign in, just slowly.
func (s *Server) loginDelay(ctx context.Context, username string, now time.Time) time.Duration {
	fails, err := s.store.FailedLoginTimes(ctx, username, "", now.Add(-(lockWindow + lockFor)))
	if err != nil || len(fails) < pairMax {
		return 0
	}
	d := time.Duration(len(fails)-pairMax+1) * 500 * time.Millisecond
	return min(d, maxDelay)
}

// checkLoginThrottle runs both layers for one attempt. It returns a message
// to show the user when the attempt must be refused, or "" to go ahead
// (after any delay). action names the audit event ("auth.login", ...).
func (s *Server) checkLoginThrottle(r *http.Request, username, action string) string {
	ctx := r.Context()
	ip := middleware.ClientIP(r)
	now := time.Now()

	if !s.loginLimiter.allow(ip, now) {
		s.logEvent(r, nil, action+".rate_limited", "user", username, "")
		return "Too many attempts. Wait a minute and try again."
	}
	if left := s.loginLockRemaining(ctx, username, ip, now); left > 0 {
		// Which tier tripped stays out of the message and the log (§2.2).
		s.logEvent(r, nil, action+".locked", "user", username, "")
		mins := int(math.Ceil(left.Minutes()))
		unit := "minutes"
		if mins == 1 {
			unit = "minute"
		}
		return fmt.Sprintf("Too many attempts. Try again in %d %s.", mins, unit)
	}
	if d := s.loginDelay(ctx, username, now); d > 0 {
		select {
		case <-time.After(d):
		case <-ctx.Done():
		}
	}
	return ""
}

// recordLoginResult stores the outcome for the lockout. A success clears
// that (username, IP) pair's failures.
func (s *Server) recordLoginResult(r *http.Request, username string, success bool) {
	ctx := r.Context()
	ip := middleware.ClientIP(r)
	if err := s.store.RecordLoginAttempt(ctx, username, ip, success, time.Now()); err != nil {
		log.Printf("lockout: record: %v", err)
	}
	if success {
		_ = s.store.ClearLoginFailures(ctx, username, ip)
	}
}
