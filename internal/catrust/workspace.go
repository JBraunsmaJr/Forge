package catrust

import (
	"os"
	"path"
	"path/filepath"
)

// containerCertVariables are set inside a job container to point the common
// clients at the staged bundle.
var containerCertVariables = []string{
	"SSL_CERT_FILE",       // Go, OpenSSL-based tools
	"GIT_SSL_CAINFO",      // git
	"CURL_CA_BUNDLE",      // curl
	"REQUESTS_CA_BUNDLE",  // Python requests
	"PIP_CERT",            // pip
	"NODE_EXTRA_CA_CERTS", // Node.js
	"AWS_CA_BUNDLE",       // AWS CLI and SDKs
}

// EnvVar is one environment variable to set in a job container.
type EnvVar struct {
	Name  string
	Value string
}

// StageInWorkspace copies the installed bundle into a job's workspace, where
// the job's container will find it. It does nothing when no extra
// certificates are configured.
func StageInWorkspace(workspaceDir string) error {
	source := BundlePath()
	if source == "" || workspaceDir == "" {
		return nil
	}
	content, err := os.ReadFile(source)
	if err != nil {
		return err
	}
	target := stagedPath(workspaceDir)
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	return writeFileAtomic(target, content)
}

// ContainerEnv returns the variables that make a job container use the
// bundle staged in workspaceDir, in a fixed order. It returns nil when no
// bundle has been staged there, so a container is never pointed at a file
// that does not exist.
func ContainerEnv(workspaceDir string) []EnvVar {
	if workspaceDir == "" {
		return nil
	}
	if _, err := os.Stat(stagedPath(workspaceDir)); err != nil {
		return nil
	}
	inContainer := path.Join(containerWorkspace, workspaceCertDir, bundleFileName)
	vars := make([]EnvVar, 0, len(containerCertVariables))
	for _, name := range containerCertVariables {
		vars = append(vars, EnvVar{Name: name, Value: inContainer})
	}
	return vars
}

// stagedPath is where the bundle lives inside a workspace on the host.
func stagedPath(workspaceDir string) string {
	return filepath.Join(workspaceDir, filepath.FromSlash(workspaceCertDir), bundleFileName)
}
