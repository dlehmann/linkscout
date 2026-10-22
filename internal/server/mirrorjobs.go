package server

import (
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"time"

	"github.com/dlehmann/linkscout/internal/mirror"
)

// Job states reported to the UI.
const (
	JobQueued  = "queued"  // added to the list, never started
	JobRunning = "running" // downloading
	JobDone    = "done"    // finished without error
	JobFailed  = "failed"  // finished with an error
	JobStopped = "stopped" // cancelled by the user
)

// Errors of the list operations; the handlers map them to HTTP statuses.
var (
	errJobNotFound = errors.New("job not found")
	errDirInUse    = errors.New("target directory already used by another list entry")
)

// mirrorJob is one website of the offline copy list. Every job keeps its own
// configuration (URL, target directory, user agent, credentials, limits) so
// several websites can be downloaded at the same time with different settings.
type mirrorJob struct {
	id      int
	cfg     mirror.Config // Fetch.Password stays on the server
	runner  *mirror.Mirror
	started bool
	stopped bool
}

// JobView is the JSON representation of a job in /api/state. It deliberately
// lists every field instead of embedding mirror.Config so the password can
// never leak.
type JobView struct {
	ID          int             `json:"id"`
	URL         string          `json:"url"`
	OutDir      string          `json:"outDir"`
	External    bool            `json:"external"`
	UserAgent   string          `json:"userAgent"`
	Username    string          `json:"username,omitempty"`
	Concurrency int             `json:"concurrency"`
	MaxPages    int             `json:"maxPages"`
	DelayMs     int             `json:"delayMs"`
	Status      string          `json:"status"`
	Progress    mirror.Progress `json:"progress"`

	ExternalFiles  bool     `json:"externalFiles"`
	Robots         bool     `json:"robots"`
	Sitemap        bool     `json:"sitemap"`
	Rules          []string `json:"rules,omitempty"`
	Fresh          bool     `json:"fresh"`
	RatePerHost    float64  `json:"ratePerHost,omitempty"`
	BandwidthKiB   int64    `json:"bandwidthKiB,omitempty"`
	MaxFileSizeMiB int64    `json:"maxFileSizeMiB,omitempty"`
}

// mirrorJobs is the list of offline copy jobs.
type mirrorJobs struct {
	mu     sync.Mutex
	jobs   []*mirrorJob
	nextID int
}

// add appends a job without starting it. Two entries must not share a target
// directory, they would overwrite each other's files. With unique=true (the
// directory was not chosen by the user) a free name is picked instead by
// appending -2, -3, … to the directory.
func (l *mirrorJobs) add(cfg mirror.Config, unique bool) (*mirrorJob, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	abs, err := filepath.Abs(cfg.OutDir)
	if err != nil {
		return nil, err
	}
	dir := abs
	for n := 2; l.dirInUse(dir); n++ {
		if !unique {
			return nil, errDirInUse
		}
		dir = fmt.Sprintf("%s-%d", abs, n)
	}
	cfg.OutDir = dir
	l.nextID++
	j := &mirrorJob{id: l.nextID, cfg: cfg, runner: &mirror.Mirror{}}
	l.jobs = append(l.jobs, j)
	return j, nil
}

// dirInUse reports whether a list entry already writes into dir. l.mu must
// be held.
func (l *mirrorJobs) dirInUse(dir string) bool {
	for _, j := range l.jobs {
		if j.cfg.OutDir == dir {
			return true
		}
	}
	return false
}

// find returns the job with the given id or nil. l.mu must be held.
func (l *mirrorJobs) find(id int) *mirrorJob {
	for _, j := range l.jobs {
		if j.id == id {
			return j
		}
	}
	return nil
}

// start starts the job with the given id, or – with id 0 – every job that is
// not running and has not finished successfully yet.
func (l *mirrorJobs) start(id int) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if id != 0 {
		j := l.find(id)
		if j == nil {
			return errJobNotFound
		}
		return j.start()
	}
	var firstErr error
	for _, j := range l.jobs {
		if st := j.status(); st == JobRunning || st == JobDone {
			continue
		}
		if err := j.start(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// start runs the job (again). l.mu must be held.
func (j *mirrorJob) start() error {
	if err := j.runner.Start(j.cfg); err != nil {
		return err
	}
	j.started, j.stopped = true, false
	return nil
}

// stop cancels the job with the given id, or – with id 0 – every running job.
func (l *mirrorJobs) stop(id int) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, j := range l.jobs {
		if (id == 0 || j.id == id) && j.runner.Progress().Running {
			j.stopped = true
			j.runner.Stop()
		}
	}
	if id != 0 && l.find(id) == nil {
		return errJobNotFound
	}
	return nil
}

// remove stops the job and drops it from the list. With done=true (and id 0)
// every job that finished successfully is removed instead.
func (l *mirrorJobs) remove(id int, done bool) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	keep := l.jobs[:0]
	found := false
	for _, j := range l.jobs {
		drop := (id != 0 && j.id == id) || (done && j.status() == JobDone)
		if drop {
			found = true
			j.stopped = true
			j.runner.Stop()
			continue
		}
		keep = append(keep, j)
	}
	// Clear the tail so dropped jobs can be garbage collected.
	for i := len(keep); i < len(l.jobs); i++ {
		l.jobs[i] = nil
	}
	l.jobs = keep
	if id != 0 && !found {
		return errJobNotFound
	}
	return nil
}

// finishedDir returns the target directory of the job with the given id or,
// with id 0, of the most recently finished job. Only successfully finished
// jobs count (used by GET /api/mirror/zip).
func (l *mirrorJobs) finishedDir(id int) (string, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	var best *mirrorJob
	var bestAt time.Time
	for _, j := range l.jobs {
		if (id != 0 && j.id != id) || j.status() != JobDone {
			continue
		}
		if p := j.runner.Progress(); best == nil || p.FinishedAt.After(bestAt) {
			best, bestAt = j, p.FinishedAt
		}
	}
	if best == nil {
		return "", false
	}
	return best.cfg.OutDir, true
}

// password returns the password of the newest job with this user name, so an
// empty password field in the UI can reuse it (see Server.fetchOptions).
func (l *mirrorJobs) password(user string) string {
	l.mu.Lock()
	defer l.mu.Unlock()
	for i := len(l.jobs) - 1; i >= 0; i-- {
		if f := l.jobs[i].cfg.Fetch; f.Username == user && f.Password != "" {
			return f.Password
		}
	}
	return ""
}

// shutdown stops every running job and waits (at most timeout) until they
// have cleaned up their temporary directories.
func (l *mirrorJobs) shutdown(timeout time.Duration) {
	l.mu.Lock()
	runners := make([]*mirror.Mirror, 0, len(l.jobs))
	for _, j := range l.jobs {
		j.runner.Stop()
		runners = append(runners, j.runner)
	}
	l.mu.Unlock()
	done := make(chan struct{})
	go func() {
		for _, r := range runners {
			r.Wait()
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(timeout):
	}
}

// running reports whether any job is downloading.
func (l *mirrorJobs) running() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, j := range l.jobs {
		if j.runner.Progress().Running {
			return true
		}
	}
	return false
}

// views returns the JSON representation of all jobs in list order.
func (l *mirrorJobs) views() []JobView {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]JobView, 0, len(l.jobs))
	for _, j := range l.jobs {
		out = append(out, JobView{
			ID:          j.id,
			URL:         j.cfg.StartURL,
			OutDir:      j.cfg.OutDir,
			External:    j.cfg.External,
			UserAgent:   j.cfg.Fetch.UserAgent,
			Username:    j.cfg.Fetch.Username,
			Concurrency: j.cfg.Concurrency,
			MaxPages:    j.cfg.MaxPages,
			DelayMs:     int(j.cfg.Delay / time.Millisecond),
			Status:      j.status(),
			Progress:    j.runner.Progress(),

			ExternalFiles:  j.cfg.ExternalFiles,
			Robots:         j.cfg.Robots,
			Sitemap:        j.cfg.Sitemap,
			Rules:          j.cfg.Rules,
			Fresh:          j.cfg.Fresh,
			RatePerHost:    j.cfg.Fetch.RatePerHost,
			BandwidthKiB:   j.cfg.Fetch.Bandwidth >> 10,
			MaxFileSizeMiB: j.cfg.MaxFileSize >> 20,
		})
	}
	return out
}

// status derives the job state from the runner's progress.
func (j *mirrorJob) status() string {
	p := j.runner.Progress()
	switch {
	case !j.started:
		return JobQueued
	case p.Running:
		return JobRunning
	case j.stopped:
		return JobStopped
	case p.Error != "":
		return JobFailed
	default:
		return JobDone
	}
}
