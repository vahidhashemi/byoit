package main

import (
	"encoding/json"
	"fmt"
	"time"
)

// OpenLDAPService handles OpenLDAP-specific operations
type OpenLDAPService struct {
	helmManager *HelmManager
	config      Config
}

// NewOpenLDAPService creates a new OpenLDAP service instance
func NewOpenLDAPService(helmManager *HelmManager, config Config) *OpenLDAPService {
	return &OpenLDAPService{
		helmManager: helmManager,
		config:      config,
	}
}

// Install installs OpenLDAP using Helm
func (ols *OpenLDAPService) Install() error {
	// Add Helm repository
	repoName := "helm-openldap"
	if err := ols.helmManager.AddRepository(repoName, ols.config.OpenLDAPRepo); err != nil {
		return fmt.Errorf("failed to add helm repository: %v", err)
	}
	
	// Set chart reference
	chartRef := ols.config.OpenLDAPChart
	if ols.config.OpenLDAPVer != "" {
		chartRef = fmt.Sprintf("%s:%s", ols.config.OpenLDAPChart, ols.config.OpenLDAPVer)
	}
	
	// Prepare values for the jpgouin/openldap chart
	values := ols.buildHelmValues()
	
	fmt.Printf("DEBUG: Installing with values:\n")
	fmt.Printf("  Domain: %s\n", ols.config.Domain)
	fmt.Printf("  BaseDN: %s\n", ols.config.BaseDN)
	fmt.Printf("  Admin Password: %s\n", ols.config.AdminPass)
	fmt.Printf("  Config Password: %s\n", ols.config.ConfigPass)
	
	// Debug: print the actual values being sent to Helm
	valuesJSON, _ := json.MarshalIndent(values, "", "  ")
	fmt.Printf("DEBUG: Helm values JSON:\n%s\n", string(valuesJSON))
	
	// Install the chart
	if err := ols.helmManager.InstallChart(ols.config.ReleaseName, chartRef, ols.config.Namespace, values); err != nil {
		return fmt.Errorf("failed to install OpenLDAP chart: %v", err)
	}
	
	// Check deployment status manually
	fmt.Println(">> Checking deployment status...")
	time.Sleep(10 * time.Second) // Give pods time to start
	
	// Check if pods are running
	args := kube("--kubeconfig", ols.config.Kubeconfig, "get", "pods", "-n", ols.config.Namespace, "-l", "app.kubernetes.io/instance="+ols.config.ReleaseName, "--no-headers")
	if out, err := runOut(args[0], args[1:]...); err == nil && out != "" {
		fmt.Printf("DEBUG: Pod status:\n%s\n", out)
	}
	
	// Get the actual admin password from the secret
	actualPassword := ols.helmManager.GetActualAdminPassword(ols.config.ReleaseName, ols.config.Namespace, ols.config.Kubeconfig)
	if actualPassword != "" {
		fmt.Printf("DEBUG: Actual admin password from secret: %s\n", actualPassword)
	}
	
	return nil
}

// buildHelmValues builds the Helm values for the OpenLDAP chart
func (ols *OpenLDAPService) buildHelmValues() map[string]interface{} {
	return map[string]interface{}{
		"global": map[string]interface{}{
			"ldapDomain":    ols.config.Domain,
			"adminPassword": ols.config.AdminPass,
			"configPassword": ols.config.ConfigPass,
		},
		"ldap": map[string]interface{}{
			"domain": ols.config.Domain,
			"root":   ols.config.BaseDN,
		},
		"image": map[string]interface{}{
			"repository": "docker.io/jpgouin/openldap",
			"tag":        "2.6.9-fix",
			"pullPolicy": "IfNotPresent",
		},
		// Set pull policy to IfNotPresent for air-gapped mode
		"initSchema": map[string]interface{}{
			"image": map[string]interface{}{
				"repository": "docker.io/library/debian",
				"tag":        "latest",
				"pullPolicy": "IfNotPresent",
			},
		},
		"initTlsSecret": map[string]interface{}{
			"image": map[string]interface{}{
				"repository": "docker.io/alpine/openssl",
				"tag":        "latest",
				"pullPolicy": "IfNotPresent",
			},
		},
		"ltbPasswd": map[string]interface{}{
			"image": map[string]interface{}{
				"repository": "docker.io/tiredofit/self-service-password",
				"tag":        "5.2.3",
				"pullPolicy": "IfNotPresent",
			},
		},
		// Try alternative parameter names for ltb-passwd
		"ltb-passwd": map[string]interface{}{
			"image": map[string]interface{}{
				"repository": "docker.io/tiredofit/self-service-password",
				"tag":        "5.2.3",
				"pullPolicy": "IfNotPresent",
			},
		},
		"ltbpasswd": map[string]interface{}{
			"image": map[string]interface{}{
				"repository": "docker.io/tiredofit/self-service-password",
				"tag":        "5.2.3",
				"pullPolicy": "IfNotPresent",
			},
		},
		// Try direct image name
		"ltbPasswdImage":           "docker.io/tiredofit/self-service-password:5.2.3",
		"ltbPasswdImagePullPolicy": "IfNotPresent",
		// Try other common parameter names
		"ltbPasswdImageName":       "docker.io/tiredofit/self-service-password:5.2.3",
		"ltbPasswdImageRepository": "docker.io/tiredofit/self-service-password",
		"ltbPasswdImageTag":        "5.2.3",
		"phpldapadmin": map[string]interface{}{
			"image": map[string]interface{}{
				"repository": "docker.io/osixia/phpldapadmin",
				"tag":        "0.9.0",
				"pullPolicy": "IfNotPresent",
			},
		},
	}
}

// GetConnectionInfo returns connection information for the OpenLDAP service
func (ols *OpenLDAPService) GetConnectionInfo() {
	actualPassword := ols.helmManager.GetActualAdminPassword(ols.config.ReleaseName, ols.config.Namespace, ols.config.Kubeconfig)
	if actualPassword != "" {
		fmt.Printf("DEBUG: Actual admin password from secret: %s\n", actualPassword)
	}
	
	fmt.Printf("\nThen test LDAP connection:\n")
	fmt.Printf("  kubectl -n %s exec statefulset/ldap -- ldapsearch -x -H ldap://ldap:389 -D cn=admin,%s -w '%s' -b '%s' -s base\n", 
		ols.config.Namespace, ols.config.BaseDN, ols.config.AdminPass, ols.config.BaseDN)
	fmt.Printf("\nExternal access (port-forwarding active):\n")
	fmt.Printf("  phpLDAPadmin: http://localhost:9876\n")
	fmt.Printf("  LDAP: ldap://localhost:3890\n")
	fmt.Printf("  Test LDAP: ldapsearch -x -H ldap://localhost:3890 -D cn=admin,%s -w '%s' -b '%s' -s base\n", ols.config.BaseDN, ols.config.AdminPass, ols.config.BaseDN)
	fmt.Printf("\nNote: The OpenLDAP is configured with base DN '%s' and admin password '%s'\n", ols.config.BaseDN, ols.config.AdminPass)
}
