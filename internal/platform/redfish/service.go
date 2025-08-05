// SPDX-FileCopyrightText: 2025 The Kepler Authors
// SPDX-License-Identifier: Apache-2.0

package redfish

import (
	"context"
	"crypto/tls"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/stmcginnis/gofish"
	"github.com/sustainable-computing-io/kepler/internal/service"
)

// Service implements the Redfish power monitoring service
type Service struct {
	logger *slog.Logger
	bmc    *BMCDetail        // Store BMC configuration
	client *gofish.APIClient // Direct gofish client

	powerReader *PowerReader
	nodeID      string
	bmcID       string // Store BMC ID for metrics

	// Data collection
	mu             sync.RWMutex
	lastReading    *PowerReading
	lastUpdateTime time.Time

	// Service lifecycle
	running bool
	stopCh  chan struct{}
}

// Ensure Service implements the required interfaces
var (
	_ service.Service     = (*Service)(nil)
	_ service.Initializer = (*Service)(nil)
	_ service.Runner      = (*Service)(nil)
	_ service.Shutdowner  = (*Service)(nil)
)

// NewService creates a new Redfish service
func NewService(configPath, nodeID string, logger *slog.Logger) (*Service, error) {
	// Log experimental feature warning
	logger = logger.With(slog.String("service", "experimental.redfish"))
	logger.Warn("Using EXPERIMENTAL Redfish power monitoring feature", "feature", "redfish", "node_id", nodeID)

	// Load BMC configuration
	cfg, err := Load(configPath)
	if err != nil {
		return nil, fmt.Errorf("failed to load BMC configuration: %w", err)
	}

	logger.Info("Resolved node identifier", "node_id", nodeID)

	// Get BMC details and ID for this node
	bmcDetail, err := cfg.BMCForNode(nodeID)
	if err != nil {
		return nil, fmt.Errorf("failed to get BMC configuration for node %s: %w", nodeID, err)
	}

	bmcID, err := cfg.BMCIDForNode(nodeID)
	if err != nil {
		return nil, fmt.Errorf("failed to get BMC ID for node %s: %w", nodeID, err)
	}

	logger.Info("BMC configuration loaded", "node_id", nodeID, "bmc_id", bmcID, "endpoint", bmcDetail.Endpoint)

	// Create power reader (will be initialized in Init())
	reader := NewPowerReader(logger)

	return &Service{
		logger:      logger,
		bmc:         bmcDetail,
		powerReader: reader,
		nodeID:      nodeID,
		bmcID:       bmcID,
		stopCh:      make(chan struct{}),
	}, nil
}

// Name returns the service name
func (s *Service) Name() string {
	return "platform.redfish"
}

// Init initializes the service by connecting to the BMC
func (s *Service) Init() error {
	s.logger.Info("Initializing Redfish power monitoring service",
		"node_id", s.nodeID,
		"bmc_endpoint", s.bmc.Endpoint)

	// Validate credentials - if one is provided, both must be provided
	if (s.bmc.Username == "" && s.bmc.Password != "") ||
		(s.bmc.Username != "" && s.bmc.Password == "") {
		return fmt.Errorf("both username and password must be provided for authentication")
	}

	// Create HTTP client with timeout and TLS configuration
	httpClient := &http.Client{
		Timeout: 30 * time.Second,
	}

	// Configure TLS settings if insecure flag is set
	if s.bmc.Insecure {
		httpClient.Transport = &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		}
	}

	// Configure Gofish client
	gofishConfig := gofish.ClientConfig{
		Endpoint:   s.bmc.Endpoint,
		Username:   s.bmc.Username,
		Password:   s.bmc.Password,
		HTTPClient: httpClient,
	}

	// Use context.Background() for client connection since gofish stores this context
	// and uses it for all subsequent HTTP requests. A timeout context would cause
	// "context canceled" errors on later requests when the timeout expires.
	client, err := gofish.ConnectContext(context.Background(), gofishConfig)
	if err != nil {
		// Don't log credentials in error messages
		return fmt.Errorf("failed to connect to BMC at %s for node %s: %w", s.bmc.Endpoint, s.nodeID, err)
	}

	s.client = client

	// Initialize power reader with the connected client
	s.powerReader.SetClient(client)

	// Validate that power reading is available during initialization
	s.logger.Info("Validating power reading capability", "node_id", s.nodeID)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	_, err = s.powerReader.ReadPower(ctx)
	if err != nil {
		return fmt.Errorf("power reading validation failed for BMC at %s: %w", s.bmc.Endpoint, err)
	}

	s.logger.Info("Successfully connected to BMC with power reading capability validated", "node_id", s.nodeID)
	return nil
}

// Run starts the power monitoring loop
func (s *Service) Run(ctx context.Context) error {
	s.mu.Lock()
	s.running = true
	s.mu.Unlock()

	s.logger.Info("Starting Redfish power monitoring loop", "node_id", s.nodeID)

	// Collection interval: every 10 seconds
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			s.logger.Info("Redfish power monitoring stopped due to context cancellation")
			return ctx.Err()
		case <-s.stopCh:
			s.logger.Info("Redfish power monitoring stopped")
			return nil
		case <-ticker.C:
			if err := s.collectPowerData(ctx); err != nil {
				s.logger.Error("Failed to collect power data", "error", err)
				// Continue monitoring despite errors
			}
		}
	}
}

// Shutdown cleanly shuts down the service
func (s *Service) Shutdown() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.running {
		return nil
	}

	s.logger.Info("Shutting down Redfish power monitoring service")

	close(s.stopCh)

	// Disconnect gofish client if connected
	if s.client != nil {
		s.client.Logout()
		s.client = nil
	}

	s.running = false

	s.logger.Info("Redfish power monitoring service shutdown complete")
	return nil
}

// GetLatestReading returns the most recent power reading
func (s *Service) LatestReading() (*PowerReading, string) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return s.lastReading, s.nodeID
}

// BMCID returns the BMC ID for metrics labeling
func (s *Service) BMCID() string {
	return s.bmcID
}

// collectPowerData collects power data from the BMC with retry logic
func (s *Service) collectPowerData(ctx context.Context) error {
	// Use retry logic: 3 attempts with 2-second delay
	reading, err := s.powerReader.ReadPowerWithRetry(ctx, 3, 2*time.Second)
	if err != nil {
		return fmt.Errorf("failed to read power from BMC: %w", err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	s.lastReading = reading
	s.lastUpdateTime = reading.Timestamp

	return nil
}

// IsRunning returns true if the service is currently running
func (s *Service) IsRunning() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.running
}
