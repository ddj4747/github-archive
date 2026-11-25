package panel

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"strings"

	"githubarchive/internal/config"
	"githubarchive/internal/model"
)

// resolvedState describes the affected ref's commit tree, not all repository
// refs at the time of an event. A missing exact SHA is never inferred by time.
type resolvedState struct {
	Available bool   `json:"available"`
	Owner     string `json:"owner"`
	Name      string `json:"name"`
	SHA       string `json:"sha"`
	Reason    string `json:"reason"`
	Ref       string `json:"ref"`
}

type statePullRequest struct {
	Merged         bool   `json:"merged"`
	MergeCommitSHA string `json:"merge_commit_sha"`
	Base           struct {
		Repo struct {
			FullName string `json:"full_name"`
		} `json:"repo"`
	} `json:"base"`
}

type statePayload struct {
	Ref         string           `json:"ref"`
	Head        string           `json:"head"`
	After       string           `json:"after"`
	Deleted     bool             `json:"deleted"`
	Action      string           `json:"action"`
	PullRequest statePullRequest `json:"pull_request"`
}

func (p *Server) eventState(w http.ResponseWriter, r *http.Request) {
	if !onlyGet(w, r) {
		return
	}
	scope, id := r.URL.Query().Get("scope"), r.URL.Query().Get("event_id")
	if scope == "" || id == "" || len(scope) > 512 || len(id) > 256 {
		http.Error(w, "invalid event", 400)
		return
	}
	var typ, owner, name, raw string
	err := p.db.QueryRowContext(r.Context(), `SELECT event_type,repo_owner,repo_name,raw FROM activity_events WHERE scope=? AND event_id=?`, scope, id).Scan(&typ, &owner, &name, &raw)
	if err == sql.ErrNoRows {
		http.Error(w, "event not found", 404)
		return
	}
	if err != nil {
		http.Error(w, "event unavailable", 500)
		return
	}
	result := resolvedState{Owner: owner, Name: name}
	var event struct {
		Payload statePayload `json:"payload"`
	}
	if err = json.Unmarshal([]byte(raw), &event); err != nil {
		result.Reason = "The recorded event payload is invalid."
		jsonReply(w, result)
		return
	}
	result.Ref = event.Payload.Ref
	var candidate string
	switch typ {
	case "PushEvent":
		if event.Payload.Deleted {
			result.Reason = "The pushed ref was deleted; it has no state after deletion."
			jsonReply(w, result)
			return
		}
		head, after := event.Payload.Head, event.Payload.After
		if head != "" && after != "" && head != after {
			result.Reason = "The event has conflicting commit IDs."
			jsonReply(w, result)
			return
		}
		candidate = head
		if candidate == "" {
			candidate = after
		}
	case "PullRequestEvent":
		pr := event.Payload.PullRequest
		if event.Payload.Action != "closed" || !pr.Merged {
			result.Reason = "This pull request event does not record a completed merge."
			jsonReply(w, result)
			return
		}
		if pr.Base.Repo.FullName != "" && !strings.EqualFold(pr.Base.Repo.FullName, owner+"/"+name) {
			result.Reason = "The recorded merge belongs to a different repository."
			jsonReply(w, result)
			return
		}
		candidate = pr.MergeCommitSHA
	default:
		result.Reason = "This event does not identify an exact repository commit."
		jsonReply(w, result)
		return
	}
	if candidate == "" {
		result.Reason = "This event does not include an exact commit ID."
		jsonReply(w, result)
		return
	}
	if allZeroSHA(candidate) {
		result.Reason = "The ref was deleted; it has no state after deletion."
		jsonReply(w, result)
		return
	}
	p.finishState(r, &result, candidate)
	jsonReply(w, result)
}

func (p *Server) refState(w http.ResponseWriter, r *http.Request) {
	if !onlyGet(w, r) {
		return
	}
	q := r.URL.Query()
	owner, name, ref, old, newSHA := q.Get("owner"), q.Get("name"), q.Get("ref_name"), q.Get("old_sha"), q.Get("new_sha")
	if !model.ValidSegment(owner) || !model.ValidSegment(name) || ref == "" || len(ref) > 4096 || (!validObjectID(old) && old != "") || (!validObjectID(newSHA) && newSHA != "") {
		http.Error(w, "invalid ref change", 400)
		return
	}
	side := q.Get("side")
	if side == "" {
		side = "after"
	}
	if side != "after" && side != "before" {
		http.Error(w, "invalid state side", 400)
		return
	}
	var storedOwner, storedName, storedRef, storedOld, storedNew string
	err := p.db.QueryRowContext(r.Context(), `SELECT owner,name,ref_name,old_sha,new_sha FROM ref_changes WHERE owner=? COLLATE NOCASE AND name=? COLLATE NOCASE AND ref_name=? AND old_sha=? AND new_sha=?`, owner, name, ref, old, newSHA).Scan(&storedOwner, &storedName, &storedRef, &storedOld, &storedNew)
	if err == sql.ErrNoRows {
		http.Error(w, "ref change not found", 404)
		return
	}
	if err != nil {
		http.Error(w, "ref change unavailable", 500)
		return
	}
	result := resolvedState{Owner: storedOwner, Name: storedName, Ref: storedRef}
	candidate := storedNew
	if side == "before" {
		candidate = storedOld
	}
	if candidate == "" || allZeroSHA(candidate) {
		if side == "before" {
			result.Reason = "The ref did not exist before this change."
		} else {
			result.Reason = "The ref was deleted; it has no state after deletion."
		}
		jsonReply(w, result)
		return
	}
	p.finishState(r, &result, candidate)
	jsonReply(w, result)
}

func allZeroSHA(s string) bool {
	return (len(s) == 40 || len(s) == 64) && strings.Trim(s, "0") == ""
}

// finishState verifies the ID, peels annotated tags, and requires that the
// resulting commit is reachable from the archived refs, including keep refs.
func (p *Server) finishState(r *http.Request, s *resolvedState, candidate string) {
	if !validObjectID(candidate) {
		s.Reason = "The recorded commit ID is invalid or incomplete."
		return
	}
	if !model.ValidSegment(s.Owner) || !model.ValidSegment(s.Name) {
		s.Reason = "The event does not identify an archived repository."
		return
	}
	owner, name := p.canonicalRepository(r, s.Owner, s.Name)
	m, err := p.mirrorDirFor(model.Repository{Owner: owner, Name: name})
	if err != nil {
		s.Reason = "The repository mirror is not available locally."
		return
	}
	out, err := localOutput(r.Context(), m, 256, "rev-parse", "--verify", "--end-of-options", candidate+"^{commit}")
	if err != nil {
		s.Reason = "The recorded commit is not available in this mirror."
		return
	}
	sha := strings.TrimSpace(string(out))
	if !validObjectID(sha) || !reachableCommit(r.Context(), m, sha) {
		s.Reason = "The recorded commit is not retained by this mirror."
		return
	}
	s.Owner, s.Name, s.SHA, s.Available = owner, name, sha, true
}

func (p *Server) canonicalRepository(r *http.Request, owner, name string) (string, string) {
	if c, err := config.LoadForEditing(p.configPath); err == nil {
		for _, repo := range c.Repositories {
			if strings.EqualFold(repo.Owner, owner) && strings.EqualFold(repo.Name, name) {
				return repo.Owner, repo.Name
			}
		}
	}
	var o, n string
	if err := p.db.QueryRowContext(r.Context(), `SELECT owner,name FROM discovered_repositories WHERE owner=? COLLATE NOCASE AND name=? COLLATE NOCASE LIMIT 1`, owner, name).Scan(&o, &n); err == nil {
		return o, n
	}
	// Removed explicit targets may have no discovery row, but their recorded
	// ref changes still preserve the spelling of the original mirror path.
	rows, err := p.db.QueryContext(r.Context(), `SELECT DISTINCT owner,name FROM ref_changes WHERE owner=? COLLATE NOCASE AND name=? COLLATE NOCASE LIMIT 2`, owner, name)
	if err != nil {
		return owner, name
	}
	defer rows.Close()
	if rows.Next() && rows.Scan(&o, &n) == nil && !rows.Next() && rows.Err() == nil {
		return o, n
	}
	return owner, name
}
