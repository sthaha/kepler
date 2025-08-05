// SPDX-FileCopyrightText: 2025 The Kepler Authors
// SPDX-License-Identifier: Apache-2.0

package redfish

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sustainable-computing-io/kepler/internal/platform/redfish/mock"
)

func TestNewService(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))

	tt := []struct {
		name          string
		configContent string
		nodeID        string
		kubeNodeName  string
		expectError   bool
	}{{
		name: "ValidConfiguration",
		configContent: `
nodes:
  test-node: test-bmc
bmcs:
  test-bmc:
    endpoint: "https://192.168.1.100"
    username: "admin"
    password: "password"
    insecure: true
`,
		nodeID:       "test-node",
		kubeNodeName: "",
		expectError:  false,
	}, {
		name: "NodeNotFound",
		configContent: `
nodes:
  other-node: test-bmc
bmcs:
  test-bmc:
    endpoint: "https://192.168.1.100"
    username: "admin"
    password: "password"
    insecure: true
`,
		nodeID:       "missing-node",
		kubeNodeName: "",
		expectError:  true,
	}, {
		name: "InvalidConfigFile",
		configContent: `
invalid: yaml: content
`,
		nodeID:       "test-node",
		kubeNodeName: "",
		expectError:  true,
	}, {
		name: "HostnameFallback",
		configContent: `
nodes:
  test-hostname: test-bmc
bmcs:
  test-bmc:
    endpoint: "https://192.168.1.100"
    username: "admin"
    password: "password"
    insecure: true
`,
		nodeID:       "",
		kubeNodeName: "",
		expectError:  true, // Should fail because we don't implement hostname fallback in the constructor
	}}

	for _, tc := range tt {
		t.Run(tc.name, func(t *testing.T) {
			// Create temporary config file
			tmpDir, err := os.MkdirTemp("", "service_test")
			require.NoError(t, err)
			defer func() { _ = os.RemoveAll(tmpDir) }()

			configFile := filepath.Join(tmpDir, "config.yaml")
			err = os.WriteFile(configFile, []byte(tc.configContent), 0644)
			require.NoError(t, err)

			// Create service
			service, err := NewService(configFile, tc.nodeID, logger)

			if tc.expectError {
				assert.Error(t, err)
				assert.Nil(t, service)
				return
			}

			require.NoError(t, err)
			require.NotNil(t, service)

			// Verify service properties
			assert.Equal(t, "platform.redfish", service.Name())
			assert.False(t, service.IsRunning())
			assert.Nil(t, service.client) // Client is created during Init()
			assert.NotNil(t, service.powerReader)
			assert.NotNil(t, service.stopCh)

			// Verify resolved node ID
			assert.Equal(t, tc.nodeID, service.nodeID)
		})
	}
}

func TestNewServiceNonExistentConfig(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))

	service, err := NewService("/non/existent/config.yaml", "test-node", logger)
	assert.Error(t, err)
	assert.Nil(t, service)
	assert.Contains(t, err.Error(), "failed to load BMC configuration")
}

func TestServiceInitSuccess(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	scenario := mock.TestScenario{
		Config: mock.ServerConfig{
			Vendor:     mock.VendorGeneric,
			Username:   "admin",
			Password:   "password",
			PowerWatts: 150.0,
			EnableAuth: true,
		},
	}

	server := mock.CreateScenarioServer(scenario)
	defer server.Close()

	// Create service with mock server
	service := createTestService(t, server, logger)

	// Test initialization
	err := service.Init()
	assert.NoError(t, err)
	assert.NotNil(t, service.client)

	// Cleanup
	err = service.Shutdown()
	assert.NoError(t, err)
}

func TestServiceInitConnectionFailure(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	scenario := mock.TestScenario{
		Config: mock.ServerConfig{
			Vendor:     mock.VendorGeneric,
			Username:   "admin",
			Password:   "password",
			PowerWatts: 150.0,
			EnableAuth: true,
			ForceError: mock.ErrorAuth,
		},
	}

	server := mock.CreateScenarioServer(scenario)
	defer server.Close()

	// Create service with failing mock server
	service := createTestService(t, server, logger)

	// Test initialization failure
	err := service.Init()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to connect to BMC")
	assert.Nil(t, service.client)
}

func TestServiceRunAndShutdown(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	scenario := mock.TestScenario{
		Config: mock.ServerConfig{
			Vendor:     mock.VendorGeneric,
			Username:   "admin",
			Password:   "password",
			PowerWatts: 200.0,
			EnableAuth: true,
		},
	}

	server := mock.CreateScenarioServer(scenario)
	defer server.Close()

	// Create and initialize service
	service := createTestService(t, server, logger)
	err := service.Init()
	require.NoError(t, err)

	// Start service in background
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var runErr error
	var wg sync.WaitGroup

	wg.Add(1)
	go func() {
		defer wg.Done()
		runErr = service.Run(ctx)
	}()

	// Wait a bit for service to start
	time.Sleep(100 * time.Millisecond)
	assert.True(t, service.IsRunning())

	// Shutdown service
	err = service.Shutdown()
	assert.NoError(t, err)

	// Wait for Run to complete
	wg.Wait()
	assert.NoError(t, runErr)
	assert.False(t, service.IsRunning())
	assert.Nil(t, service.client)
}

func TestServiceRunWithContextCancellation(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	scenario := mock.TestScenario{
		Config: mock.ServerConfig{
			Vendor:     mock.VendorGeneric,
			Username:   "admin",
			Password:   "password",
			PowerWatts: 175.0,
			EnableAuth: true,
		},
	}

	server := mock.CreateScenarioServer(scenario)
	defer server.Close()

	// Create and initialize service
	service := createTestService(t, server, logger)
	err := service.Init()
	require.NoError(t, err)

	// Start service with short timeout
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	err = service.Run(ctx)
	assert.Equal(t, context.DeadlineExceeded, err)

	// Cleanup
	err = service.Shutdown()
	assert.NoError(t, err)
}

func TestServicePowerDataCollection(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	initialPower := 150.0
	scenario := mock.TestScenario{
		Config: mock.ServerConfig{
			Vendor:     mock.VendorGeneric,
			Username:   "admin",
			Password:   "password",
			PowerWatts: initialPower,
			EnableAuth: true,
		},
	}

	server := mock.CreateScenarioServer(scenario)
	defer server.Close()

	// Create and initialize service
	service := createTestService(t, server, logger)
	err := service.Init()
	require.NoError(t, err)

	// Test initial state (no readings yet)
	reading, nodeID := service.LatestReading()
	assert.Nil(t, reading)
	assert.Equal(t, "test-node", nodeID)

	// Collect some power data manually
	ctx := context.Background()
	err = service.collectPowerData(ctx)
	assert.NoError(t, err)

	// Check first reading
	reading, nodeID = service.LatestReading()
	require.NotNil(t, reading)
	assert.InDelta(t, initialPower, reading.Watts, 0.001)
	assert.Equal(t, "test-node", nodeID)

	// Change power and collect again
	newPower := 250.0
	server.SetPowerWatts(newPower)

	// Wait a bit to ensure time difference
	time.Sleep(10 * time.Millisecond)

	err = service.collectPowerData(ctx)
	assert.NoError(t, err)

	// Check second reading
	reading, nodeID = service.LatestReading()
	require.NotNil(t, reading)
	assert.InDelta(t, newPower, reading.Watts, 0.001)
	assert.Equal(t, "test-node", nodeID)

	// Cleanup
	err = service.Shutdown()
	assert.NoError(t, err)
}

func TestServiceCollectionErrors(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	scenario := mock.TestScenario{
		Config: mock.ServerConfig{
			Vendor:     mock.VendorGeneric,
			Username:   "admin",
			Password:   "password",
			PowerWatts: 150.0,
			EnableAuth: true,
			ForceError: mock.ErrorMissingChassis,
		},
	}

	server := mock.CreateScenarioServer(scenario)
	defer server.Close()

	// Create and initialize service (should succeed)
	service := createTestService(t, server, logger)
	err := service.Init()
	require.NoError(t, err)

	// Try to collect power data (should fail)
	ctx := context.Background()
	err = service.collectPowerData(ctx)
	assert.Error(t, err)

	// Verify no data was stored
	reading, _ := service.LatestReading()
	assert.Nil(t, reading)

	// Cleanup
	err = service.Shutdown()
	assert.NoError(t, err)
}

func TestServiceConcurrentAccess(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	scenario := mock.TestScenario{
		Config: mock.ServerConfig{
			Vendor:     mock.VendorGeneric,
			Username:   "admin",
			Password:   "password",
			PowerWatts: 180.0,
			EnableAuth: true,
		},
	}

	server := mock.CreateScenarioServer(scenario)
	defer server.Close()

	// Create and initialize service
	service := createTestService(t, server, logger)
	err := service.Init()
	require.NoError(t, err)

	// Collect initial data
	ctx := context.Background()
	err = service.collectPowerData(ctx)
	assert.NoError(t, err)

	// Test concurrent reads
	const numReaders = 10
	var wg sync.WaitGroup

	for i := 0; i < numReaders; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				reading, nodeID := service.LatestReading()
				if reading != nil {
					assert.InDelta(t, 180.0, reading.Watts, 0.001)
					assert.Equal(t, "test-node", nodeID)
				}
			}
		}()
	}

	// Concurrent data collection
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 10; i++ {
			_ = service.collectPowerData(ctx)
			time.Sleep(10 * time.Millisecond)
		}
	}()

	wg.Wait()

	// Cleanup
	err = service.Shutdown()
	assert.NoError(t, err)
}

func TestServiceShutdownIdempotent(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	scenario := mock.TestScenario{
		Config: mock.ServerConfig{
			Vendor:     mock.VendorGeneric,
			Username:   "admin",
			Password:   "password",
			PowerWatts: 150.0,
			EnableAuth: true,
		},
	}

	server := mock.CreateScenarioServer(scenario)
	defer server.Close()

	// Create and initialize service
	service := createTestService(t, server, logger)
	err := service.Init()
	require.NoError(t, err)

	// First shutdown
	err = service.Shutdown()
	assert.NoError(t, err)
	assert.False(t, service.IsRunning())

	// Second shutdown should be safe
	err = service.Shutdown()
	assert.NoError(t, err)
	assert.False(t, service.IsRunning())

	// Third shutdown should also be safe
	err = service.Shutdown()
	assert.NoError(t, err)
	assert.False(t, service.IsRunning())
}

func TestServiceIntegrationWithDifferentVendors(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	vendors := []mock.VendorType{
		mock.VendorDell,
		mock.VendorHPE,
		mock.VendorLenovo,
		mock.VendorGeneric,
	}

	for _, vendor := range vendors {
		t.Run(string(vendor), func(t *testing.T) {
			scenario := mock.TestScenario{
				Config: mock.ServerConfig{
					Vendor:     vendor,
					Username:   "admin",
					Password:   "password",
					PowerWatts: 165.5,
					EnableAuth: true,
				},
			}

			server := mock.CreateScenarioServer(scenario)
			defer server.Close()

			// Create and test service
			service := createTestService(t, server, logger)

			// Init
			err := service.Init()
			require.NoError(t, err)

			// Collect data
			ctx := context.Background()
			err = service.collectPowerData(ctx)
			assert.NoError(t, err)

			// Verify reading
			reading, _ := service.LatestReading()
			require.NotNil(t, reading)
			assert.InDelta(t, 165.5, reading.Watts, 0.001)

			// Cleanup
			err = service.Shutdown()
			assert.NoError(t, err)
		})
	}
}

func TestServiceInterfaceCompliance(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	scenario := mock.TestScenario{
		Config: mock.ServerConfig{
			Vendor:     mock.VendorGeneric,
			Username:   "admin",
			Password:   "password",
			PowerWatts: 150.0,
			EnableAuth: true,
		},
	}

	server := mock.CreateScenarioServer(scenario)
	defer server.Close()

	service := createTestService(t, server, logger)

	// Test Service interface
	assert.Equal(t, "platform.redfish", service.Name())

	// Test Initializer interface
	err := service.Init()
	assert.NoError(t, err)

	// Test Runner interface (with quick timeout)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	err = service.Run(ctx)
	assert.Equal(t, context.DeadlineExceeded, err)

	// Test Shutdowner interface
	err = service.Shutdown()
	assert.NoError(t, err)
}

func TestServiceGetBMCID(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	scenario := mock.TestScenario{
		Config: mock.ServerConfig{
			Vendor:     mock.VendorGeneric,
			Username:   "admin",
			Password:   "password",
			PowerWatts: 100.0,
			EnableAuth: true,
		},
	}

	server := mock.CreateScenarioServer(scenario)
	defer server.Close()

	service := createTestService(t, server, logger)

	// Test GetBMCID returns the configured BMC ID
	bmcID := service.BMCID()
	assert.Equal(t, "test-bmc", bmcID)
}

func TestResolveNodeIDPriorities(t *testing.T) {
	testCases := []struct {
		name          string
		redfishNodeID string
		kubeNodeName  string
		expected      string
		expectError   bool
	}{
		{
			name:          "CLI flag takes priority",
			redfishNodeID: "cli-node",
			kubeNodeName:  "kube-node",
			expected:      "cli-node",
		},
		{
			name:          "Kube node name used when CLI flag empty",
			redfishNodeID: "",
			kubeNodeName:  "kube-node",
			expected:      "kube-node",
		},
		{
			name:          "Kube node name used when CLI flag whitespace",
			redfishNodeID: "  \t\n  ",
			kubeNodeName:  "kube-node",
			expected:      "kube-node",
		},
		{
			name:          "Hostname fallback when both empty",
			redfishNodeID: "",
			kubeNodeName:  "",
			// expected will be set to actual hostname in test
		},
		{
			name:          "Trimmed values",
			redfishNodeID: "  cli-node  ",
			kubeNodeName:  "  kube-node  ",
			expected:      "cli-node",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			result, err := ResolveNodeID(tc.redfishNodeID, tc.kubeNodeName)

			if tc.expectError {
				assert.Error(t, err)
				return
			}

			assert.NoError(t, err)

			if tc.expected == "" {
				// For hostname fallback test
				hostname, err := os.Hostname()
				require.NoError(t, err)
				assert.Equal(t, hostname, result)
			} else {
				assert.Equal(t, tc.expected, result)
			}
		})
	}
}

func TestServiceInitCredentialValidation(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))

	testCases := []struct {
		name     string
		username string
		password string
		wantErr  bool
	}{
		{
			name:     "Both username and password provided",
			username: "admin",
			password: "secret",
			wantErr:  false,
		},
		{
			name:     "Both username and password empty",
			username: "",
			password: "",
			wantErr:  false,
		},
		{
			name:     "Username without password",
			username: "admin",
			password: "",
			wantErr:  true,
		},
		{
			name:     "Password without username",
			username: "",
			password: "secret",
			wantErr:  true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Create temporary config file with specific credentials
			tmpDir, err := os.MkdirTemp("", "credential_test")
			require.NoError(t, err)
			t.Cleanup(func() { _ = os.RemoveAll(tmpDir) })

			configContent := fmt.Sprintf(`
nodes:
  test-node: test-bmc
bmcs:
  test-bmc:
    endpoint: "https://192.168.1.100"
    username: "%s"
    password: "%s"
    insecure: true
`, tc.username, tc.password)

			configFile := filepath.Join(tmpDir, "config.yaml")
			err = os.WriteFile(configFile, []byte(configContent), 0644)
			require.NoError(t, err)

			// Create service
			service, err := NewService(configFile, "test-node", logger)
			require.NoError(t, err)

			// Test initialization
			err = service.Init()

			if tc.wantErr {
				assert.Error(t, err)
				assert.Contains(t, err.Error(), "both username and password must be provided")
			} else {
				// Init may fail due to connection issues, but not credential validation
				if err != nil {
					assert.NotContains(t, err.Error(), "both username and password must be provided")
				}
			}
		})
	}
}

// Helper function to create a test service with mock server
func createTestService(t *testing.T, server *mock.Server, logger *slog.Logger) *Service {
	// Create temporary config file
	tmpDir, err := os.MkdirTemp("", "service_test")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(tmpDir) })

	configContent := `
nodes:
  test-node: test-bmc
bmcs:
  test-bmc:
    endpoint: "` + server.URL() + `"
    username: "admin"
    password: "password"
    insecure: true
`

	configFile := filepath.Join(tmpDir, "config.yaml")
	err = os.WriteFile(configFile, []byte(configContent), 0644)
	require.NoError(t, err)

	// Create service
	service, err := NewService(configFile, "test-node", logger)
	require.NoError(t, err)

	return service
}
