package main

import (
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"helm.sh/helm/v3/pkg/action"
	"helm.sh/helm/v3/pkg/chart/loader"
	"helm.sh/helm/v3/pkg/cli"
	"helm.sh/helm/v3/pkg/getter"
	"helm.sh/helm/v3/pkg/repo"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

// HelmManager handles all Helm operations
type HelmManager struct {
	config *action.Configuration
}

// NewHelmManager creates a new Helm manager instance
func NewHelmManager(kubeconfigPath, namespace string) (*HelmManager, error) {
	config, err := getHelmActionConfig(kubeconfigPath, namespace)
	if err != nil {
		return nil, fmt.Errorf("failed to create helm manager: %v", err)
	}
	
	return &HelmManager{config: config}, nil
}

// AddRepository adds a Helm repository
func (hm *HelmManager) AddRepository(name, url string) error {
	repoFile := cli.New().RepositoryConfig
	
	// Create repo entry
	entry := &repo.Entry{
		Name: name,
		URL:  url,
	}
	
	// Add repository
	r, err := repo.NewChartRepository(entry, getter.All(cli.New()))
	if err != nil {
		return fmt.Errorf("failed to create chart repository: %v", err)
	}
	
	// Try to download index file, if it fails, try downloading locally first
	fmt.Printf("DEBUG: Attempting to download index file from %s\n", url)
	if _, err := r.DownloadIndexFile(); err != nil {
		fmt.Printf("DEBUG: Failed to download index file directly: %v\n", err)
		fmt.Printf("DEBUG: Attempting to download index file locally...\n")
		
		// Download the index file locally using curl
		indexURL := url + "/index.yaml"
		cmd := exec.Command("curl", "-s", "-L", indexURL)
		output, err := cmd.Output()
		if err != nil {
			return fmt.Errorf("failed to download index file locally: %v", err)
		}
		
		// Write the index file to the cache directory
		cacheDir := cli.New().RepositoryCache
		indexFile := filepath.Join(cacheDir, name+"-index.yaml")
		
		// Ensure cache directory exists
		if err := os.MkdirAll(cacheDir, 0755); err != nil {
			return fmt.Errorf("failed to create cache directory: %v", err)
		}
		
		// Write the downloaded index file
		if err := os.WriteFile(indexFile, output, 0644); err != nil {
			return fmt.Errorf("failed to write index file: %v", err)
		}
		
		fmt.Printf("DEBUG: Successfully downloaded index file locally to %s\n", indexFile)
	}
	
	// Update repository file
	repoFileObj, err := repo.LoadFile(repoFile)
	if err != nil {
		repoFileObj = &repo.File{}
	}
	
	repoFileObj.Update(entry)
	if err := repoFileObj.WriteFile(repoFile, 0644); err != nil {
		return fmt.Errorf("failed to write repository file: %v", err)
	}
	
	fmt.Printf("✅ Added Helm repository: %s\n", name)
	return nil
}

// InstallChart installs a Helm chart with the given values
func (hm *HelmManager) InstallChart(releaseName, chartRef, namespace string, values map[string]interface{}) error {
	// Always do a fresh install (no upgrade logic to avoid breaking things)
	fmt.Printf("DEBUG: Installing fresh release: %s\n", releaseName)
	installAction := action.NewInstall(hm.config)
	installAction.ReleaseName = releaseName
	installAction.Namespace = namespace
	installAction.CreateNamespace = true
	installAction.Wait = false  // Don't wait for all resources to be ready
	installAction.Timeout = 5 * time.Minute  // Shorter timeout
	
	// Locate chart
	cp, err := installAction.LocateChart(chartRef, cli.New())
	if err != nil {
		return fmt.Errorf("failed to locate chart: %v", err)
	}
	
	// Load chart
	chart, err := loader.Load(cp)
	if err != nil {
		return fmt.Errorf("failed to load chart: %v", err)
	}
	
	// Install the chart
	helmRelease, err := installAction.Run(chart, values)
	if err != nil {
		return fmt.Errorf("failed to install chart: %v", err)
	}
	
	fmt.Printf("✅ Successfully installed/upgraded release: %s\n", helmRelease.Name)
	return nil
}

// GetActualAdminPassword retrieves the actual admin password from the Kubernetes secret
func (hm *HelmManager) GetActualAdminPassword(releaseName, namespace, kubeconfigPath string) string {
	args := kube("--kubeconfig", kubeconfigPath, "get", "secret", releaseName, "-n", namespace, "-o", "jsonpath={.data.LDAP_ADMIN_PASSWORD}")
	if out, err := runOut(args[0], args[1:]...); err == nil && out != "" {
		// Decode base64
		decoded, err := base64.StdEncoding.DecodeString(out)
		if err == nil {
			return string(decoded)
		}
	}
	return ""
}

// getKubeConfig returns a Kubernetes client configuration
func getKubeConfig(kubeconfigPath string) (*rest.Config, error) {
	fmt.Printf("DEBUG: Getting kubeconfig from: %s\n", kubeconfigPath)
	if kubeconfigPath != "" {
		config, err := clientcmd.BuildConfigFromFlags("", kubeconfigPath)
		if err != nil {
			return nil, fmt.Errorf("failed to build config from kubeconfig file: %v", err)
		}
		fmt.Printf("DEBUG: Successfully loaded kubeconfig, server: %s\n", config.Host)
		return config, nil
	}
	return rest.InClusterConfig()
}

// getHelmActionConfig creates a Helm action configuration
func getHelmActionConfig(kubeconfigPath, namespace string) (*action.Configuration, error) {
	fmt.Printf("DEBUG: Creating Helm action config for namespace: %s\n", namespace)
	config := &action.Configuration{}
	
	// Get Kubernetes config
	kubeConfig, err := getKubeConfig(kubeconfigPath)
	if err != nil {
		return nil, fmt.Errorf("failed to get kube config: %v", err)
	}
	
	// Create Kubernetes client (not used directly but required for Helm)
	_, err = kubernetes.NewForConfig(kubeConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create kubernetes client: %v", err)
	}
	fmt.Printf("DEBUG: Successfully created Kubernetes client\n")
	
	// Create a custom REST client getter that uses our kubeconfig
	restClientGetter := &genericclioptions.ConfigFlags{
		KubeConfig: &kubeconfigPath,
		Namespace:  &namespace,
	}
	
	// Initialize action configuration
	fmt.Printf("DEBUG: Initializing Helm action configuration...\n")
	if err := config.Init(restClientGetter, namespace, "secret", func(format string, v ...interface{}) {
		fmt.Printf(format, v...)
	}); err != nil {
		return nil, fmt.Errorf("failed to initialize helm action config: %v", err)
	}
	fmt.Printf("DEBUG: Successfully initialized Helm action configuration\n")
	
	return config, nil
}
