// SPDX-FileCopyrightText: 2025 The Kepler Authors
// SPDX-License-Identifier: Apache-2.0

package redfish

import (
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// BMCConfig represents the configuration structure for BMC connections
type BMCConfig struct {
	Nodes map[string]string    `yaml:"nodes"` // Node name -> BMC ID mapping
	BMCs  map[string]BMCDetail `yaml:"bmcs"`  // BMC ID -> BMC connection details
}

// BMCDetail contains the connection details for a specific BMC
type BMCDetail struct {
	Endpoint string `yaml:"endpoint"` // BMC endpoint URL
	Username string `yaml:"username"` // BMC username
	Password string `yaml:"password"` // BMC password
	Insecure bool   `yaml:"insecure"` // Skip TLS verification
}

// Load loads and parses the BMC configuration file
func Load(configPath string) (*BMCConfig, error) {
	data, err := os.ReadFile(configPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read BMC config file %s: %w", configPath, err)
	}

	var config BMCConfig
	if err := yaml.Unmarshal(data, &config); err != nil {
		return nil, fmt.Errorf("failed to parse BMC config file %s: %w", configPath, err)
	}

	return &config, nil
}

// BMCForNode returns the BMC details for a given node name
func (c *BMCConfig) BMCForNode(nodeName string) (*BMCDetail, error) {
	bmcID, exists := c.Nodes[nodeName]
	if !exists {
		return nil, fmt.Errorf("node %s not found in BMC configuration", nodeName)
	}

	bmcDetail, exists := c.BMCs[bmcID]
	if !exists {
		return nil, fmt.Errorf("BMC %s not found in BMC configuration", bmcID)
	}

	return &bmcDetail, nil
}

// BMCIDForNode returns the BMC ID for a given node name
func (c *BMCConfig) BMCIDForNode(nodeName string) (string, error) {
	bmcID, exists := c.Nodes[nodeName]
	if !exists {
		return "", fmt.Errorf("node %s not found in BMC configuration", nodeName)
	}

	_, exists = c.BMCs[bmcID]
	if !exists {
		return "", fmt.Errorf("BMC %s not found in BMC configuration", bmcID)
	}

	return bmcID, nil
}

// ResolveNodeID resolves the node identifier using the following precedence:
// 1. CLI flag / config.yaml (--experimental.platform.redfish.node-id)
// 2. Kubernetes node name
// 3. Hostname fallback
func ResolveNodeID(redfishNodeID, kubeNodeName string) (string, error) {
	// Priority 1: CLI flag
	if strings.TrimSpace(redfishNodeID) != "" {
		return strings.TrimSpace(redfishNodeID), nil
	}

	// Priority 2: Kubernetes node name
	if strings.TrimSpace(kubeNodeName) != "" {
		return strings.TrimSpace(kubeNodeName), nil
	}

	// TODO: consider if this fallback is appropriate
	// Priority 3: Hostname fallback
	hostname, err := os.Hostname()
	if err != nil {
		return "", fmt.Errorf("failed to determine node identifier: %w", err)
	}

	return hostname, nil
}
