package redfish

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBMCIDForNodeSuccess(t *testing.T) {
	// Create temporary config file
	tmpDir, err := os.MkdirTemp("", "config_test")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(tmpDir) })

	configContent := `
nodes:
  node1: bmc1
  node2: bmc2
bmcs:
  bmc1:
    endpoint: "https://bmc1.example.com"
  bmc2:
    endpoint: "https://bmc2.example.com"
`

	configFile := filepath.Join(tmpDir, "config.yaml")
	err = os.WriteFile(configFile, []byte(configContent), 0644)
	require.NoError(t, err)

	config, err := Load(configFile)
	require.NoError(t, err)

	tt := []struct {
		name     string
		nodeID   string
		expected string
		wantErr  bool
	}{{
		name:     "Valid node1",
		nodeID:   "node1",
		expected: "bmc1",
		wantErr:  false,
	}, {
		name:     "Valid node2",
		nodeID:   "node2",
		expected: "bmc2",
		wantErr:  false,
	}, {
		name:    "Non-existent node",
		nodeID:  "node3",
		wantErr: true,
	}, {
		name:    "Empty node ID",
		nodeID:  "",
		wantErr: true,
	}}

	for _, tc := range tt {
		t.Run(tc.name, func(t *testing.T) {
			result, err := config.BMCIDForNode(tc.nodeID)

			if tc.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tc.expected, result)
			}
		})
	}
}

func TestBMCForNodeEdgeCases(t *testing.T) {
	// Create temporary config with edge cases
	tmpDir, err := os.MkdirTemp("", "config_edge_test")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(tmpDir) })

	configContent := `
nodes:
  node1: bmc1
  node2: nonexistent-bmc  # BMC that doesn't exist in bmcs section
bmcs:
  bmc1:
    endpoint: "https://bmc1.example.com"
    username: "admin"
    password: "secret"
    insecure: true
`

	configFile := filepath.Join(tmpDir, "config.yaml")
	err = os.WriteFile(configFile, []byte(configContent), 0644)
	require.NoError(t, err)

	config, err := Load(configFile)
	require.NoError(t, err)

	tt := []struct {
		name    string
		nodeID  string
		wantErr bool
	}{{
		name:    "Node with non-existent BMC reference",
		nodeID:  "node2",
		wantErr: true,
	}, {
		name:    "Valid node",
		nodeID:  "node1",
		wantErr: false,
	}}

	for _, tc := range tt {
		t.Run(tc.name, func(t *testing.T) {
			_, err := config.BMCForNode(tc.nodeID)

			if tc.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}
