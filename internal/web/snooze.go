package web

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/thingzio/devbox/internal/hub"
	"github.com/thingzio/devbox/internal/store"
)

// snoozeFor maps the snooze menu's choices to durations; zero means until the
// PR changes. Every snooze also ends when the bucket or head commit changes.
var snoozeFor = map[string]time.Duration{"change": 0, "1d": 24 * time.Hour, "3d": 72 * time.Hour, "7d": 7 * 24 * time.Hour}

func prFromForm(r *http.Request) (int64, int, bool) {
	repoID, err1 := strconv.ParseInt(r.PostFormValue("repo_id"), 10, 64)
	number, err2 := strconv.Atoi(r.PostFormValue("number"))
	return repoID, number, err1 == nil && err2 == nil && repoID > 0 && number > 0
}

func (s *Server) handleSnooze(w http.ResponseWriter, r *http.Request) {
	repoID, number, ok := prFromForm(r)
	d, known := snoozeFor[r.PostFormValue("for")]
	if !ok || !known {
		http.Error(w, "invalid snooze", http.StatusBadRequest)
		return
	}
	now := s.o.Now()
	var until *time.Time
	if d > 0 {
		t := now.Add(d)
		until = &t
	}
	err := s.o.Store.Snooze(r.Context(), repoID, number, until, now)
	if errors.Is(err, store.ErrNotFound) {
		http.Error(w, "pull request not found", http.StatusNotFound)
		return
	}
	if err != nil {
		s.serverError(w, err)
		return
	}
	s.o.Log.Info("pull request snoozed", "repo_id", repoID, "pr", number, "for", r.PostFormValue("for"))
	s.snoozeChanged(w, r, repoID, number)
}

func (s *Server) handleUnsnooze(w http.ResponseWriter, r *http.Request) {
	repoID, number, ok := prFromForm(r)
	if !ok {
		http.Error(w, "invalid pull request", http.StatusBadRequest)
		return
	}
	if err := s.o.Store.Unsnooze(r.Context(), repoID, number); err != nil {
		s.serverError(w, err)
		return
	}
	s.o.Log.Info("pull request unsnoozed", "repo_id", repoID, "pr", number)
	s.snoozeChanged(w, r, repoID, number)
}

// snoozeChanged refreshes other open pages and returns to the page the form came from.
func (s *Server) snoozeChanged(w http.ResponseWriter, r *http.Request, repoID int64, number int) {
	s.o.Hub.Publish(hub.Event{RepoID: repoID, Number: number})
	http.Redirect(w, r, localPath(r.PostFormValue("next")), http.StatusSeeOther) //nolint:gosec // localPath allows only paths on this server
}
