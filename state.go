package hxdna

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/joho/godotenv"
)

// State holds the enrollment result persisted on disk after a successful enroll.
type State struct {
	WorkerID    string
	OrgID       string
	NatsURL     string
	ControlURL  string
	EnrolledAt  string
	Environment string
}

// LoadState reads state from ~/.{dirName}/.env.
// dirName should be unique per worker type (e.g. ".my-worker").
func LoadState(dirName string) (*State, error) {
	dir, err := stateDir(dirName)
	if err != nil {
		return nil, err
	}
	path := filepath.Join(dir, ".env")
	env, err := godotenv.Read(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("not enrolled — run: worker enroll <bootstrap>")
		}
		return nil, fmt.Errorf("reading state: %w", err)
	}
	s := &State{
		WorkerID:    env["WORKER_ID"],
		OrgID:       env["ORG_ID"],
		NatsURL:     env["NATS_URL"],
		ControlURL:  env["CONTROL_URL"],
		EnrolledAt:  env["ENROLLED_AT"],
		Environment: env["ENVIRONMENT"],
	}
	if s.WorkerID == "" || s.OrgID == "" || s.NatsURL == "" || s.ControlURL == "" {
		return nil, fmt.Errorf("state is incomplete — re-enroll with: worker enroll <bootstrap>")
	}
	return s, nil
}

// SaveState writes state to ~/.{dirName}/.env, creating the directory if needed.
func SaveState(dirName string, s *State) error {
	dir, err := stateDir(dirName)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("creating state dir: %w", err)
	}
	env := map[string]string{
		"WORKER_ID":   s.WorkerID,
		"ORG_ID":      s.OrgID,
		"NATS_URL":    s.NatsURL,
		"CONTROL_URL": s.ControlURL,
		"ENROLLED_AT": s.EnrolledAt,
		"ENVIRONMENT": s.Environment,
	}
	path := filepath.Join(dir, ".env")
	if err := godotenv.Write(env, path); err != nil {
		return fmt.Errorf("writing state: %w", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return fmt.Errorf("setting state file permissions: %w", err)
	}
	return nil
}

func stateDir(dirName string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, dirName), nil
}
