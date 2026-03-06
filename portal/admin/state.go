package admin

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	"gosuda.org/portal/portal/policy"
)

type stateStore struct {
	path string
}

type settings struct {
	ApprovalMode   policy.Mode `json:"approval_mode"`
	ApprovedLeases []string    `json:"approved_leases,omitempty"`
	DeniedLeases   []string    `json:"denied_leases,omitempty"`
	BannedLeases   []string    `json:"banned_leases,omitempty"`
	BannedIPs      []string    `json:"banned_ips,omitempty"`
}

func newStateStore(path string) *stateStore {
	if path == "" {
		path = "admin_settings.json"
	}
	return &stateStore{path: path}
}

func (s *stateStore) Load(runtime *policy.Runtime) error {
	if s == nil || runtime == nil {
		return nil
	}

	root, name, err := s.openRoot()
	if err != nil {
		return err
	}
	defer root.Close()

	data, err := root.ReadFile(name)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}

	var payload settings
	if err := json.Unmarshal(data, &payload); err != nil {
		return err
	}
	if payload.ApprovalMode != "" {
		if err := runtime.Approver().SetMode(payload.ApprovalMode); err != nil {
			return err
		}
	}
	for _, leaseID := range payload.ApprovedLeases {
		runtime.Approver().Approve(leaseID)
	}
	for _, leaseID := range payload.DeniedLeases {
		runtime.Approver().Deny(leaseID)
	}
	for _, leaseID := range payload.BannedLeases {
		runtime.BanLease(leaseID)
	}
	runtime.IPFilter().SetBannedIPs(payload.BannedIPs)
	return nil
}

func (s *stateStore) Save(runtime *policy.Runtime) error {
	if s == nil || runtime == nil {
		return nil
	}

	payload := settings{
		ApprovalMode:   runtime.Approver().Mode(),
		ApprovedLeases: runtime.Approver().ApprovedLeases(),
		DeniedLeases:   runtime.Approver().DeniedLeases(),
		BannedLeases:   runtime.BannedLeases(),
		BannedIPs:      runtime.IPFilter().BannedIPs(),
	}
	data, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return err
	}

	root, name, err := s.openRoot()
	if err != nil {
		return err
	}
	defer root.Close()
	return root.WriteFile(name, data, 0o600)
}

func (s *stateStore) openRoot() (*os.Root, string, error) {
	path := s.path
	if path == "" {
		path = "admin_settings.json"
	}

	dir := filepath.Dir(path)
	name := filepath.Base(path)
	if dir == "" {
		dir = "."
	}

	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, "", err
	}
	return root, name, nil
}
