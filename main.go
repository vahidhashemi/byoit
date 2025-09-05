package main

import (
	"bufio"
	"bytes"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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

type Config struct {
	Namespace     string `json:"namespace"`
	NetworkCIDR   string `json:"networkCidr"`     // e.g. 192.168.100.0/24 (reserved for upcoming NetPolicy/ServiceLB source-ranges)
	Domain        string `json:"domain"`          // e.g. armani.lab
	BaseDN        string `json:"baseDn"`          // computed from Domain: dc=armani,dc=lab
	AdminPass     string `json:"adminPass"`       // for cn=admin,<BaseDN>
	ConfigPass    string `json:"configPass"`      // for cn=admin,cn=config (can reuse AdminPass, but keep separate for clarity)
	Kubeconfig    string `json:"kubeconfig"`      // /etc/rancher/k3s/k3s.yaml
	ReleaseName   string `json:"releaseName"`     // ldap
	OpenLDAPRepo  string `json:"openldapRepo"`    // https://jp-gouin.github.io/helm-openldap/
	OpenLDAPChart string `json:"openldapChart"`   // helm-openldap/openldap-stack-ha
	OpenLDAPVer   string `json:"openldapVer"`     // optional pin, e.g. 4.3.3
}

// --- embedded assets --------------------------------------------------

//go:embed assets/memberof-job.yaml.tmpl
var memberOfJobYAML string

func ask(prompt string, hidden bool) string {
	fmt.Print(prompt)
	if hidden {
		// fall back to visible if stty not available
		cmd := exec.Command("bash", "-lc", "read -s REPLY; echo $REPLY")
		cmd.Stdin = os.Stdin
		out, _ := cmd.Output()
		fmt.Println()
		return strings.TrimSpace(string(out))
	}
	reader := bufio.NewReader(os.Stdin)
	text, _ := reader.ReadString('\n')
	return strings.TrimSpace(text)
}

func baseDNFromDomain(d string) string {
	parts := strings.Split(strings.TrimSpace(d), ".")
	var out []string
	for _, p := range parts {
		if p != "" {
			out = append(out, "dc="+p)
		}
	}
	return strings.Join(out, ",")
}

func validateCIDR(cidr string) error {
	if cidr == "" {
		return errors.New("CIDR cannot be empty")
	}
	
	// Parse the CIDR
	ip, ipNet, err := net.ParseCIDR(cidr)
	if err != nil {
		return fmt.Errorf("invalid CIDR format: %v", err)
	}
	
	// Check if it's a valid network (not a host)
	if ip.String() != ipNet.IP.String() {
		return errors.New("CIDR must represent a network, not a host address")
	}
	
	// Check if it's a private network (recommended for local infrastructure)
	if !isPrivateNetwork(ipNet) {
		return errors.New("CIDR should be a private network (10.0.0.0/8, 172.16.0.0/12, or 192.168.0.0/16)")
	}
	
	// Check minimum subnet size (at least /24 for IPv4)
	ones, bits := ipNet.Mask.Size()
	if bits == 32 && ones > 24 { // IPv4
		return errors.New("CIDR subnet too small, use /24 or larger (e.g., /24, /23, /22)")
	}
	
	return nil
}

func isPrivateNetwork(ipNet *net.IPNet) bool {
	// Check for private IPv4 networks
	_, private10, _ := net.ParseCIDR("10.0.0.0/8")
	_, private172, _ := net.ParseCIDR("172.16.0.0/12")
	_, private192, _ := net.ParseCIDR("192.168.0.0/16")
	
	return private10.Contains(ipNet.IP) || private172.Contains(ipNet.IP) || private192.Contains(ipNet.IP)
}

func checkDependencies() error {
	fmt.Println(">> Checking system dependencies...")
	
	// Check Docker
	if which("docker") == "" {
		return errors.New("docker is not installed or not in PATH")
	}
	
	// Test Docker daemon
	cmd := exec.Command("docker", "version", "--format", "{{.Server.Version}}")
	if err := cmd.Run(); err != nil {
		return errors.New("docker daemon is not running or not accessible")
	}
	fmt.Println("✅ Docker is installed and running")
	
	return nil
}

func must(err error) {
	if err != nil {
		fmt.Fprintf(os.Stderr, "fatal: %v\n", err)
		os.Exit(1)
	}
}

func which(bin string) string {
	paths := strings.Split(os.Getenv("PATH"), string(os.PathListSeparator))
	for _, p := range paths {
		full := filepath.Join(p, bin)
		if st, err := os.Stat(full); err == nil && st.Mode()&0111 != 0 {
			return full
		}
	}
	return ""
}

func run(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func runOut(name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	var b bytes.Buffer
	cmd.Stdout = &b
	cmd.Stderr = os.Stderr
	err := cmd.Run()
	return strings.TrimSpace(b.String()), err
}

func kube(args ...string) []string {
	// Use k3s binary from the same directory as the byoit binary
	execPath, _ := os.Executable()
	execDir := filepath.Dir(execPath)
	k3sPath := filepath.Join(execDir, "k3s")
	
	result := append([]string{k3sPath, "kubectl"}, args...)
	fmt.Printf("DEBUG: Using local k3s kubectl, command: %v\n", result)
	return result
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

// addHelmRepo adds a Helm repository
func addHelmRepo(name, url string) error {
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

func applyYAML(kubeconfig, yaml string) error {
	f := filepath.Join(os.TempDir(), fmt.Sprintf("infractl-%d.yaml", time.Now().UnixNano()))
	fmt.Printf("DEBUG: Writing YAML to temporary file: %s\n", f)
	if err := os.WriteFile(f, []byte(yaml), 0o600); err != nil {
		return err
	}
	defer os.Remove(f)
	
	// Debug: show first few lines of the YAML
	lines := strings.Split(yaml, "\n")
	fmt.Printf("DEBUG: YAML content (first 10 lines):\n")
	for i, line := range lines {
		if i >= 10 {
			break
		}
		fmt.Printf("  %d: %s\n", i+1, line)
	}
	
	args := kube("--kubeconfig", kubeconfig, "apply", "-f", f)
	return run(args[0], args[1:]...)
}

func ensureNS(kubeconfig, ns string) {
	args := kube("--kubeconfig", kubeconfig, "create", "ns", ns)
	_ = run(args[0], args[1:]...) // idempotent
}

// installOpenLDAP installs OpenLDAP using Helm SDK
func installOpenLDAP(cfg Config) error {
	// Add Helm repository
	repoName := "helm-openldap"
	if err := addHelmRepo(repoName, cfg.OpenLDAPRepo); err != nil {
		return fmt.Errorf("failed to add helm repository: %v", err)
	}
	
	// Get Helm action configuration
	actionConfig, err := getHelmActionConfig(cfg.Kubeconfig, cfg.Namespace)
	if err != nil {
		return fmt.Errorf("failed to get helm action config: %v", err)
	}
	
	// Set chart reference
	chartRef := cfg.OpenLDAPChart
	if cfg.OpenLDAPVer != "" {
		chartRef = fmt.Sprintf("%s:%s", cfg.OpenLDAPChart, cfg.OpenLDAPVer)
	}
	
	// Prepare values - use the correct parameter names for the jpgouin/openldap chart
	values := map[string]interface{}{
		"global": map[string]interface{}{
			"ldapDomain": cfg.Domain,
			"adminPassword": cfg.AdminPass,
			"configPassword": cfg.ConfigPass,
		},
		"ldap": map[string]interface{}{
			"domain": cfg.Domain,
			"root": cfg.BaseDN,
		},
		"image": map[string]interface{}{
            "repository": "docker.io/jpgouin/openldap",
            "tag": "2.6.9-fix",
            "pullPolicy": "IfNotPresent",
        },
		// Main OpenLDAP container configuration (working parameters)
		//"imageName": "docker.io/jpgouin/openldap:2.6.9-fix",
		//"imagePullPolicy": "IfNotPresent",
		// Set pull policy to IfNotPresent for air-gapped mode
		"initSchema": map[string]interface{}{
			"image": map[string]interface{}{
				"repository": "docker.io/library/debian",
				"tag": "latest",
				"pullPolicy": "IfNotPresent",
			},
		},
		"initTlsSecret": map[string]interface{}{
			"image": map[string]interface{}{
				"repository": "docker.io/alpine/openssl",
				"tag": "latest",
				"pullPolicy": "IfNotPresent",
			},
		},
		"ltbPasswd": map[string]interface{}{
			"image": map[string]interface{}{
				"repository": "docker.io/tiredofit/self-service-password",
				"tag": "5.2.3",
				"pullPolicy": "IfNotPresent",
			},
		},
		// Try alternative parameter names for ltb-passwd
		"ltb-passwd": map[string]interface{}{
			"image": map[string]interface{}{
				"repository": "docker.io/tiredofit/self-service-password",
				"tag": "5.2.3",
				"pullPolicy": "IfNotPresent",
			},
		},
		"ltbpasswd": map[string]interface{}{
			"image": map[string]interface{}{
				"repository": "docker.io/tiredofit/self-service-password",
				"tag": "5.2.3",
				"pullPolicy": "IfNotPresent",
			},
		},
		// Try direct image name
		"ltbPasswdImage": "docker.io/tiredofit/self-service-password:5.2.3",
		"ltbPasswdImagePullPolicy": "IfNotPresent",
		// Try other common parameter names
		"ltbPasswdImageName": "docker.io/tiredofit/self-service-password:5.2.3",
		"ltbPasswdImageRepository": "docker.io/tiredofit/self-service-password",
		"ltbPasswdImageTag": "5.2.3",
		"phpldapadmin": map[string]interface{}{
			"image": map[string]interface{}{
				"repository": "docker.io/osixia/phpldapadmin",
				"tag": "0.9.0",
				"pullPolicy": "IfNotPresent",
			},
		},
	}
	
	fmt.Printf("DEBUG: Installing with values:\n")
	fmt.Printf("  Domain: %s\n", cfg.Domain)
	fmt.Printf("  BaseDN: %s\n", cfg.BaseDN)
	fmt.Printf("  Admin Password: %s\n", cfg.AdminPass)
	fmt.Printf("  Config Password: %s\n", cfg.ConfigPass)
	
	// Debug: print the actual values being sent to Helm
	valuesJSON, _ := json.MarshalIndent(values, "", "  ")
	fmt.Printf("DEBUG: Helm values JSON:\n%s\n", string(valuesJSON))
	
	// Always do a fresh install (no upgrade logic to avoid breaking things)
	fmt.Printf("DEBUG: Installing fresh release: %s\n", cfg.ReleaseName)
	installAction := action.NewInstall(actionConfig)
	installAction.ReleaseName = cfg.ReleaseName
	installAction.Namespace = cfg.Namespace
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
	
	fmt.Printf("✅ Successfully installed/upgraded OpenLDAP release: %s\n", helmRelease.Name)
	
	// Check deployment status manually
	fmt.Println(">> Checking deployment status...")
	time.Sleep(10 * time.Second) // Give pods time to start
	
	// Check if pods are running
	args := kube("--kubeconfig", cfg.Kubeconfig, "get", "pods", "-n", cfg.Namespace, "-l", "app.kubernetes.io/instance="+cfg.ReleaseName, "--no-headers")
	if out, err := runOut(args[0], args[1:]...); err == nil && out != "" {
		fmt.Printf("DEBUG: Pod status:\n%s\n", out)
	}
	
	// Get the actual admin password from the secret
	actualPassword := getActualAdminPassword(cfg)
	if actualPassword != "" {
		fmt.Printf("DEBUG: Actual admin password from secret: %s\n", actualPassword)
	}
	
	return nil
}

// getActualAdminPassword retrieves the actual admin password from the Kubernetes secret
func getActualAdminPassword(cfg Config) string {
	args := kube("--kubeconfig", cfg.Kubeconfig, "get", "secret", cfg.ReleaseName, "-n", cfg.Namespace, "-o", "jsonpath={.data.LDAP_ADMIN_PASSWORD}")
	if out, err := runOut(args[0], args[1:]...); err == nil && out != "" {
		// Decode base64
		decoded, err := base64.StdEncoding.DecodeString(out)
		if err == nil {
			return string(decoded)
		}
	}
	return ""
}

// applyMemberOfJob applies the memberOf job YAML
func applyMemberOfJob(cfg Config) error {
	// Replace template variables in the YAML
	yamlContent := strings.ReplaceAll(memberOfJobYAML, "{{.ReleaseName}}", cfg.ReleaseName)
	yamlContent = strings.ReplaceAll(yamlContent, "{{.Namespace}}", cfg.Namespace)
	yamlContent = strings.ReplaceAll(yamlContent, "{{.BaseDN}}", cfg.BaseDN)
	yamlContent = strings.ReplaceAll(yamlContent, "{{.AdminPass}}", cfg.AdminPass)
	
	fmt.Printf("DEBUG: Applying memberOf job YAML with variables:\n")
	fmt.Printf("  ReleaseName: %s\n", cfg.ReleaseName)
	fmt.Printf("  Namespace: %s\n", cfg.Namespace)
	fmt.Printf("  BaseDN: %s\n", cfg.BaseDN)
	
	return applyYAML(cfg.Kubeconfig, yamlContent)
}

func mustJSON(v any) []byte {
	b, _ := json.MarshalIndent(v, "", "  ")
	return b
}


func main() {
	cfg := Config{
		Namespace:     "infra",
		Kubeconfig:    "/etc/rancher/k3s/k3s.yaml",
		ReleaseName:   "ldap",
		OpenLDAPRepo:  "https://jp-gouin.github.io/helm-openldap/",
		OpenLDAPChart: "helm-openldap/openldap-stack-ha",
	}

	// Check dependencies first
	fmt.Println("=== Infra bootstrap (Step 1: OpenLDAP + memberOf) ===")
	must(checkDependencies())
	
	// interactive prompts (step 1)
	
	// Validate CIDR input
	for {
		cfg.NetworkCIDR = ask("Network CIDR (e.g. 192.168.100.0/24): ", false)
		if err := validateCIDR(cfg.NetworkCIDR); err != nil {
			fmt.Printf("❌ Invalid CIDR: %v\n", err)
			fmt.Println("   Please enter a valid private network CIDR (e.g., 192.168.100.0/24)")
			continue
		}
		break
	}
	
	cfg.Domain = ask("Domain (e.g. example.lab): ", false)
	if cfg.Domain == "" {
		must(errors.New("domain is required"))
	}
	cfg.BaseDN = baseDNFromDomain(cfg.Domain)

	fmt.Printf("LDAP admin DN will be: cn=admin,%s\n", cfg.BaseDN)
	cfg.AdminPass = ask("LDAP admin password (for cn=admin,"+cfg.BaseDN+"): ", true)
	if cfg.AdminPass == "" {
		must(errors.New("admin password required"))
	}
	// config admin (cn=admin,cn=config)
	cfg.ConfigPass = ask("LDAP *config* admin password (cn=admin,cn=config) [enter to reuse same]: ", true)
	if cfg.ConfigPass == "" {
		cfg.ConfigPass = cfg.AdminPass
	}

	// preflight cluster
	fmt.Println(">> Checking k3s connectivity…")
	kubeArgs := kube("--kubeconfig", cfg.Kubeconfig, "get", "nodes", "-o", "name")
	fmt.Printf("DEBUG: Running command: %s %v\n", kubeArgs[0], kubeArgs[1:])
	out, err := runOut(kubeArgs[0], kubeArgs[1:]...)
	if err != nil || out == "" {
		fmt.Printf("DEBUG: Command output: '%s'\n", out)
		fmt.Printf("DEBUG: Command error: %v\n", err)
		must(errors.New("cannot reach cluster; verify kubeconfig and k3s"))
	}
	fmt.Printf("DEBUG: Cluster connectivity successful, output: '%s'\n", out)

	// ns + openldap
	fmt.Println(">> Ensuring namespace:", cfg.Namespace)
	ensureNS(cfg.Kubeconfig, cfg.Namespace)
	fmt.Println(">> Installing OpenLDAP via Helm SDK…")
	must(installOpenLDAP(cfg))

	// memberOf job
	fmt.Println(">> Enabling memberOf (+refint) with a post-install Job…")
	must(applyMemberOfJob(cfg))

	fmt.Println("\n✅ Done. Verify with:")
	fmt.Printf("  kubectl -n %s get pods -l app.kubernetes.io/instance=%s\n", cfg.Namespace, cfg.ReleaseName)
	fmt.Printf("  kubectl -n %s logs job/%s-memberof-setup\n", cfg.Namespace, cfg.ReleaseName)
	// Get actual admin password from secret for verification
	actualPassword := getActualAdminPassword(cfg)
	if actualPassword != "" {
		fmt.Printf("DEBUG: Actual admin password from secret: %s\n", actualPassword)
	}
	
	fmt.Printf("\nThen test LDAP connection:\n")
	fmt.Printf("  kubectl -n %s exec statefulset/ldap -- ldapsearch -x -H ldap://ldap:389 -D cn=admin,%s -w '%s' -b '%s' -s base\n", 
		cfg.Namespace, cfg.BaseDN, cfg.AdminPass, cfg.BaseDN)
	fmt.Printf("\nExternal access (port-forwarding active):\n")
	fmt.Printf("  phpLDAPadmin: http://localhost:9876\n")
	fmt.Printf("  LDAP: ldap://localhost:3890\n")
	fmt.Printf("  Test LDAP: ldapsearch -x -H ldap://localhost:3890 -D cn=admin,%s -w '%s' -b '%s' -s base\n", cfg.BaseDN, cfg.AdminPass, cfg.BaseDN)
	fmt.Printf("\nNote: The OpenLDAP is configured with base DN '%s' and admin password '%s'\n", cfg.BaseDN, cfg.AdminPass)

	// (optional) save config snapshot
	_ = os.WriteFile("infractl.step1.json", mustJSON(cfg), 0o600)
}