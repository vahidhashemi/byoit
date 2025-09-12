package main

import (
	"encoding/json"
	"fmt"
	"time"
)

// GitLabService handles GitLab-specific operations
type GitLabService struct {
	helmManager *HelmManager
	config      Config
}

// GitLabConfig holds GitLab-specific configuration
type GitLabConfig struct {
	Domain           string `json:"domain"`           // e.g., gitlab.example.com
	Hostname         string `json:"hostname"`         // e.g., gitlab.example.com
	InitialRootPassword string `json:"initialRootPassword"` // Initial root password
	SMTPEnabled      bool   `json:"smtpEnabled"`      // Whether to enable SMTP
	SMTPHost         string `json:"smtpHost"`         // SMTP host
	SMTPPort         int    `json:"smtpPort"`         // SMTP port
	SMTPUser         string `json:"smtpUser"`         // SMTP username
	SMTPPassword     string `json:"smtpPassword"`     // SMTP password
	SMTPFrom         string `json:"smtpFrom"`         // SMTP from address
	SMTPFromName     string `json:"smtpFromName"`     // SMTP from name
	EnableHTTPS      bool   `json:"enableHttps"`      // Whether to enable HTTPS
	CertManagerEmail string `json:"certManagerEmail"` // Email for Let's Encrypt
	StorageClass     string `json:"storageClass"`     // Storage class for persistent volumes
	ResourceRequests map[string]string `json:"resourceRequests"` // Resource requests
	ResourceLimits   map[string]string `json:"resourceLimits"`   // Resource limits
}

// NewGitLabService creates a new GitLab service instance
func NewGitLabService(helmManager *HelmManager, config Config, gitlabConfig GitLabConfig) *GitLabService {
	return &GitLabService{
		helmManager: helmManager,
		config:      config,
	}
}

// Install installs GitLab using Helm
func (gs *GitLabService) Install(gitlabConfig GitLabConfig) error {
	// Add GitLab Helm repository
	repoName := "gitlab"
	repoURL := "https://charts.gitlab.io/"
	if err := gs.helmManager.AddRepository(repoName, repoURL); err != nil {
		return fmt.Errorf("failed to add GitLab helm repository: %v", err)
	}
	
	// Set chart reference
	chartRef := "gitlab/gitlab"
	
	// Prepare values for the GitLab chart
	values := gs.buildHelmValues(gitlabConfig)
	
	fmt.Printf("DEBUG: Installing GitLab with values:\n")
	fmt.Printf("  Domain: %s\n", gitlabConfig.Domain)
	fmt.Printf("  Hostname: %s\n", gitlabConfig.Hostname)
	fmt.Printf("  HTTPS Enabled: %t\n", gitlabConfig.EnableHTTPS)
	
	// Debug: print the actual values being sent to Helm
	valuesJSON, _ := json.MarshalIndent(values, "", "  ")
	fmt.Printf("DEBUG: GitLab Helm values JSON:\n%s\n", string(valuesJSON))
	
	// Install the chart
	if err := gs.helmManager.InstallChart("gitlab", chartRef, gs.config.Namespace, values); err != nil {
		return fmt.Errorf("failed to install GitLab chart: %v", err)
	}
	
	// Check deployment status manually
	fmt.Println(">> Checking GitLab deployment status...")
	time.Sleep(30 * time.Second) // Give GitLab more time to start (it's a complex application)
	
	// Check if pods are running
	args := kube("--kubeconfig", gs.config.Kubeconfig, "get", "pods", "-n", gs.config.Namespace, "-l", "app.kubernetes.io/instance=gitlab", "--no-headers")
	if out, err := runOut(args[0], args[1:]...); err == nil && out != "" {
		fmt.Printf("DEBUG: GitLab pod status:\n%s\n", out)
	}
	
	return nil
}

// buildHelmValues builds the Helm values for the GitLab chart
func (gs *GitLabService) buildHelmValues(gitlabConfig GitLabConfig) map[string]interface{} {
	values := map[string]interface{}{
		"global": map[string]interface{}{
			"hosts": map[string]interface{}{
				"domain": gitlabConfig.Domain,
				"gitlab": map[string]interface{}{
					"name": gitlabConfig.Hostname,
				},
			},
			"ingress": map[string]interface{}{
				"configureCertmanager": gitlabConfig.EnableHTTPS,
				"enabled":              true,
				"tls": map[string]interface{}{
					"enabled": gitlabConfig.EnableHTTPS,
				},
			},
			"gitaly": map[string]interface{}{
				"enabled": true,
			},
			"minio": map[string]interface{}{
				"enabled": true,
			},
			"postgresql": map[string]interface{}{
				"install": true,
			},
			"redis": map[string]interface{}{
				"install": true,
			},
			"certmanager": map[string]interface{}{
				"install": gitlabConfig.EnableHTTPS,
			},
		},
		"gitlab": map[string]interface{}{
			"webservice": map[string]interface{}{
				"replicaCount": 1,
				"resources": map[string]interface{}{
					"requests": map[string]interface{}{
						"cpu":    "500m",
						"memory": "1Gi",
					},
					"limits": map[string]interface{}{
						"cpu":    "1000m",
						"memory": "2Gi",
					},
				},
			},
			"sidekiq": map[string]interface{}{
				"replicaCount": 1,
				"resources": map[string]interface{}{
					"requests": map[string]interface{}{
						"cpu":    "300m",
						"memory": "512Mi",
					},
					"limits": map[string]interface{}{
						"cpu":    "500m",
						"memory": "1Gi",
					},
				},
			},
		},
		"postgresql": map[string]interface{}{
			"primary": map[string]interface{}{
				"persistence": map[string]interface{}{
					"enabled": true,
					"size":    "8Gi",
				},
				"resources": map[string]interface{}{
					"requests": map[string]interface{}{
						"cpu":    "200m",
						"memory": "256Mi",
					},
					"limits": map[string]interface{}{
						"cpu":    "500m",
						"memory": "512Mi",
					},
				},
			},
		},
		"redis": map[string]interface{}{
			"master": map[string]interface{}{
				"persistence": map[string]interface{}{
					"enabled": true,
					"size":    "2Gi",
				},
				"resources": map[string]interface{}{
					"requests": map[string]interface{}{
						"cpu":    "100m",
						"memory": "128Mi",
					},
					"limits": map[string]interface{}{
						"cpu":    "200m",
						"memory": "256Mi",
					},
				},
			},
		},
		"minio": map[string]interface{}{
			"persistence": map[string]interface{}{
				"enabled": true,
				"size":    "10Gi",
			},
			"resources": map[string]interface{}{
				"requests": map[string]interface{}{
					"cpu":    "100m",
					"memory": "128Mi",
				},
				"limits": map[string]interface{}{
					"cpu":    "200m",
					"memory": "256Mi",
				},
			},
		},
	}
	
	// Add SMTP configuration if enabled
	if gitlabConfig.SMTPEnabled {
		values["global"].(map[string]interface{})["smtp"] = map[string]interface{}{
			"enabled": true,
			"address": fmt.Sprintf("%s:%d", gitlabConfig.SMTPHost, gitlabConfig.SMTPPort),
			"user_name": gitlabConfig.SMTPUser,
			"password": gitlabConfig.SMTPPassword,
			"from": gitlabConfig.SMTPFrom,
			"from_name": gitlabConfig.SMTPFromName,
		}
	}
	
	// Add Let's Encrypt configuration if HTTPS is enabled
	if gitlabConfig.EnableHTTPS && gitlabConfig.CertManagerEmail != "" {
		values["global"].(map[string]interface{})["ingress"].(map[string]interface{})["annotations"] = map[string]interface{}{
			"cert-manager.io/cluster-issuer": "letsencrypt-prod",
		}
	}
	
	// Add storage class if specified
	if gitlabConfig.StorageClass != "" {
		values["global"].(map[string]interface{})["storageClass"] = gitlabConfig.StorageClass
	}
	
	return values
}

// GetConnectionInfo returns connection information for the GitLab service
func (gs *GitLabService) GetConnectionInfo(gitlabConfig GitLabConfig) {
	protocol := "http"
	if gitlabConfig.EnableHTTPS {
		protocol = "https"
	}
	
	url := fmt.Sprintf("%s://%s", protocol, gitlabConfig.Hostname)
	
	fmt.Printf("\nGitLab is being deployed...\n")
	fmt.Printf("  URL: %s\n", url)
	fmt.Printf("  Initial root password: %s\n", gitlabConfig.InitialRootPassword)
	fmt.Printf("  Namespace: %s\n", gs.config.Namespace)
	
	fmt.Printf("\nTo access GitLab:\n")
	fmt.Printf("  kubectl -n %s port-forward svc/gitlab-webservice-default 8080:8080\n", gs.config.Namespace)
	fmt.Printf("  Then visit: http://localhost:8080\n")
	
	fmt.Printf("\nTo get the initial root password:\n")
	fmt.Printf("  kubectl -n %s get secret gitlab-gitlab-initial-root-password -o jsonpath='{.data.password}' | base64 -d\n", gs.config.Namespace)
	
	fmt.Printf("\nTo check GitLab status:\n")
	fmt.Printf("  kubectl -n %s get pods -l app.kubernetes.io/instance=gitlab\n", gs.config.Namespace)
}
