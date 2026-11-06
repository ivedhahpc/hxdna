package playbook

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/ivedhahpc/hxdna"
)

// memStore is an in-memory Store following the rules a SQL Store must (see ActionClaim).
type memStore struct {
	mu        sync.Mutex
	playbooks map[uuid.UUID]*Playbook
	steps     map[uuid.UUID][]*Step
	runs      map[uuid.UUID]*Run
	runSteps  map[uuid.UUID][]*RunStep
}

func newMemStore() *memStore {
	return &memStore{
		playbooks: map[uuid.UUID]*Playbook{},
		steps:     map[uuid.UUID][]*Step{},
		runs:      map[uuid.UUID]*Run{},
		runSteps:  map[uuid.UUID][]*RunStep{},
	}
}

func (s *memStore) addPlaybook(org uuid.UUID, name string, steps ...*Step) *Playbook {
	s.mu.Lock()
	defer s.mu.Unlock()
	past := time.Now().UTC().Add(-time.Hour)
	p := &Playbook{ID: uuid.New(), OrganizationID: org, Name: name, CreatedAt: past, UpdatedAt: past}
	s.playbooks[p.ID] = p
	for i, st := range steps {
		st.ID, st.OrganizationID, st.PlaybookID, st.OrderIndex = uuid.New(), org, p.ID, i
		st.CreatedAt, st.UpdatedAt = past, past
	}
	s.steps[p.ID] = steps
	return p
}

func (s *memStore) GetPlaybook(_ context.Context, org, id uuid.UUID) (*Playbook, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if p, ok := s.playbooks[id]; ok && p.OrganizationID == org {
		cp := *p
		return &cp, nil
	}
	return nil, nil
}

func (s *memStore) ListSteps(_ context.Context, org, id uuid.UUID) ([]*Step, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []*Step
	for _, st := range s.steps[id] {
		if st.OrganizationID == org {
			cp := *st
			out = append(out, &cp)
		}
	}
	return out, nil
}

func (s *memStore) CreateRun(_ context.Context, r *Run) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := *r
	s.runs[r.ID] = &cp
	return nil
}

func (s *memStore) GetRun(_ context.Context, org, id uuid.UUID) (*Run, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r, ok := s.runs[id]; ok && r.OrganizationID == org {
		cp := *r
		return &cp, nil
	}
	return nil, nil
}

func (s *memStore) run(id uuid.UUID) *Run {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := *s.runs[id]
	return &cp
}

func (s *memStore) SetRunStatus(_ context.Context, id uuid.UUID, status, errMsg string, completed bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.runs[id]
	now := time.Now().UTC()
	r.Status, r.UpdatedAt = status, now
	r.ErrorMessage = nil
	if errMsg != "" {
		r.ErrorMessage = &errMsg
	}
	if completed {
		r.CompletedAt = &now
	}
	return nil
}

func (s *memStore) ClaimAction(_ context.Context, c ActionClaim) (bool, *Run, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	me := s.runs[c.RunID]
	same := func(o *Run) bool {
		return o.ID != me.ID && o.OrganizationID == me.OrganizationID && strings.EqualFold(o.ResourceID, me.ResourceID)
	}
	for _, o := range s.runs {
		if same(o) && o.Status == RunRunning && o.UpdatedAt.Before(c.StaleBefore) {
			msg, last := InterruptedMsg, o.UpdatedAt
			o.Status, o.ErrorMessage, o.CompletedAt = RunFailed, &msg, &last
		}
	}
	var running, actedSince *Run
	for _, o := range s.runs {
		if !same(o) {
			continue
		}
		if o.Status == RunRunning {
			running = o
		} else if (o.Status == RunSucceeded || o.Status == RunFailed) && o.CompletedAt != nil && o.CompletedAt.After(me.CreatedAt) {
			if actedSince == nil || o.UpdatedAt.After(actedSince.UpdatedAt) {
				actedSince = o
			}
		}
	}
	if running != nil {
		cp := *running
		return false, &cp, nil
	}
	if actedSince != nil {
		cp := *actedSince
		return false, &cp, nil
	}
	if me.OrganizationID != c.OrgID || me.Status != c.From || (c.CheckedAfter != nil && me.UpdatedAt.Before(*c.CheckedAfter)) {
		return false, nil, nil
	}
	me.Status, me.UpdatedAt = RunRunning, time.Now().UTC()
	if c.ExecutedBy != nil {
		me.ExecutedBy = c.ExecutedBy
	}
	return true, nil, nil
}

func (s *memStore) ActingOn(_ context.Context, org uuid.UUID, resourceID string, staleBefore time.Time) (*Run, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, o := range s.runs {
		if o.OrganizationID == org && strings.EqualFold(o.ResourceID, resourceID) && o.Status == RunRunning && !o.UpdatedAt.Before(staleBefore) {
			cp := *o
			return &cp, nil
		}
	}
	return nil, nil
}

func (s *memStore) FailIfStale(_ context.Context, id uuid.UUID, inProgress, failStatus, errMsg string, cutoff time.Time) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.runs[id]
	if r == nil || r.Status != inProgress || !r.UpdatedAt.Before(cutoff) {
		return false, nil
	}
	last := r.UpdatedAt
	r.Status, r.ErrorMessage, r.CompletedAt, r.UpdatedAt = failStatus, &errMsg, &last, time.Now().UTC()
	return true, nil
}

func (s *memStore) RecentRunsOnResource(_ context.Context, org, playbookID, exclude uuid.UUID, resourceID string, since time.Time) ([]*Run, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []*Run
	for _, o := range s.runs {
		if o.OrganizationID != org || o.PlaybookID == nil || *o.PlaybookID != playbookID || o.ID == exclude || !strings.EqualFold(o.ResourceID, resourceID) {
			continue
		}
		if (o.Status == RunSucceeded && o.CompletedAt != nil && !o.CompletedAt.Before(since)) || o.Status == RunChecking || o.Status == RunRunning {
			cp := *o
			out = append(out, &cp)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i].CompletedAt, out[j].CompletedAt
		if a == nil || b == nil {
			return a == nil && b != nil
		}
		return a.After(*b)
	})
	return out, nil
}

func (s *memStore) CreateRunStep(_ context.Context, rs *RunStep) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := *rs
	s.runSteps[rs.RunID] = append(s.runSteps[rs.RunID], &cp)
	return nil
}

func (s *memStore) ListRunSteps(_ context.Context, runID uuid.UUID) ([]*RunStep, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []*RunStep
	for _, rs := range s.runSteps[runID] {
		cp := *rs
		out = append(out, &cp)
	}
	return out, nil
}

// fakeWorker answers commands by key; manifest is what Entry reads.
type fakeWorker struct {
	mu       sync.Mutex
	org      uuid.UUID
	manifest hxdna.Manifest
	answer   map[string]func(cmd hxdna.HxCommand) (map[string]any, error)
	sent     []hxdna.HxCommand
	sentTo   []uuid.UUID
}

func (w *fakeWorker) Entry(_ context.Context, org, _ uuid.UUID, stepType, key string) (*hxdna.ContractEntry, error) {
	if org != w.org {
		return nil, validationErr("worker not found")
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return FindEntry(w.manifest, "fake", stepType, key)
}

func (w *fakeWorker) Send(_ context.Context, _ *Run, workerID uuid.UUID, cmd hxdna.HxCommand, _ time.Duration) (map[string]any, error) {
	w.mu.Lock()
	w.sent = append(w.sent, cmd)
	w.sentTo = append(w.sentTo, workerID)
	f := w.answer[cmd.CommandKey]
	w.mu.Unlock()
	if f == nil {
		return nil, fmt.Errorf("worker returned error: unknown command %s", cmd.CommandKey)
	}
	return f(cmd)
}

func (w *fakeWorker) sentKeys() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	var keys []string
	for _, c := range w.sent {
		keys = append(keys, c.CommandKey)
	}
	return keys
}

// resolvingWorker picks the worker from run.Ext (a site → worker map, as Provision does).
type resolvingWorker struct {
	*fakeWorker
	bySite map[string]uuid.UUID
}

func (r *resolvingWorker) WorkerFor(_ context.Context, run *Run, _ *Step) (uuid.UUID, error) {
	site, _ := run.Ext.(string)
	id, ok := r.bySite[site]
	if !ok {
		return uuid.Nil, fmt.Errorf("site %q has no Foreman worker", site)
	}
	return id, nil
}
