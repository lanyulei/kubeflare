package application

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/lanyulei/kubeflare/internal/module/cluster/domain"
)

type kubeconfigFile struct {
	CurrentContext string                   `yaml:"current-context"`
	Clusters       []namedKubeconfigCluster `yaml:"clusters"`
	Users          []namedKubeconfigUser    `yaml:"users"`
	Contexts       []namedKubeconfigContext `yaml:"contexts"`
}

type namedKubeconfigCluster struct {
	Name    string                  `yaml:"name"`
	Cluster kubeconfigClusterConfig `yaml:"cluster"`
}

type kubeconfigClusterConfig struct {
	Server                   string `yaml:"server"`
	CertificateAuthority     string `yaml:"certificate-authority"`
	CertificateAuthorityData string `yaml:"certificate-authority-data"`
	TLSServerName            string `yaml:"tls-server-name"`
	InsecureSkipTLSVerify    bool   `yaml:"insecure-skip-tls-verify"`
	ProxyURL                 string `yaml:"proxy-url"`
	DisableCompression       bool   `yaml:"disable-compression"`
}

type namedKubeconfigUser struct {
	Name string                 `yaml:"name"`
	User kubeconfigUserAuthInfo `yaml:"user"`
}

type kubeconfigUserAuthInfo struct {
	Token                 string         `yaml:"token"`
	TokenFile             string         `yaml:"tokenFile"`
	ClientCertificate     string         `yaml:"client-certificate"`
	ClientCertificateData string         `yaml:"client-certificate-data"`
	ClientKey             string         `yaml:"client-key"`
	ClientKeyData         string         `yaml:"client-key-data"`
	Username              string         `yaml:"username"`
	Password              string         `yaml:"password"`
	AuthProvider          map[string]any `yaml:"auth-provider"`
	Exec                  map[string]any `yaml:"exec"`
	ImpersonateUser       string         `yaml:"as"`
	ImpersonateUID        string         `yaml:"as-uid"`
	ImpersonateGroups     []string       `yaml:"as-groups"`
	ImpersonateExtra      map[string]any `yaml:"as-user-extra"`
}

type namedKubeconfigContext struct {
	Name    string                  `yaml:"name"`
	Context kubeconfigContextConfig `yaml:"context"`
}

type kubeconfigContextConfig struct {
	Cluster   string `yaml:"cluster"`
	User      string `yaml:"user"`
	Namespace string `yaml:"namespace"`
}

type parsedKubeconfig struct {
	raw            string
	currentContext string
	clusters       map[string]kubeconfigClusterConfig
	users          map[string]kubeconfigUserAuthInfo
	contexts       map[string]kubeconfigContextConfig
	contextNames   []string
}

func parseKubeconfig(value string) (parsedKubeconfig, error) {
	if strings.TrimSpace(value) == "" {
		return parsedKubeconfig{}, appValidationError("kubeconfig is required")
	}

	var config kubeconfigFile
	if err := yaml.Unmarshal([]byte(value), &config); err != nil {
		return parsedKubeconfig{}, appValidationErrorWithCause("kubeconfig must be valid yaml", err)
	}

	parsed := parsedKubeconfig{
		raw:            value,
		currentContext: strings.TrimSpace(config.CurrentContext),
		clusters:       make(map[string]kubeconfigClusterConfig, len(config.Clusters)),
		users:          make(map[string]kubeconfigUserAuthInfo, len(config.Users)),
		contexts:       make(map[string]kubeconfigContextConfig, len(config.Contexts)),
		contextNames:   make([]string, 0, len(config.Contexts)),
	}
	for _, item := range config.Clusters {
		name := strings.TrimSpace(item.Name)
		if name != "" {
			parsed.clusters[name] = item.Cluster
		}
	}
	for _, item := range config.Users {
		name := strings.TrimSpace(item.Name)
		if name != "" {
			parsed.users[name] = item.User
		}
	}
	for _, item := range config.Contexts {
		name := strings.TrimSpace(item.Name)
		if name != "" {
			parsed.contexts[name] = item.Context
			parsed.contextNames = append(parsed.contextNames, name)
		}
	}
	sort.Strings(parsed.contextNames)

	if len(parsed.contexts) == 0 {
		return parsedKubeconfig{}, appValidationError("kubeconfig must contain at least one context")
	}
	return parsed, nil
}

func (c parsedKubeconfig) resolveContextName(contextName string) (string, error) {
	contextName = strings.TrimSpace(contextName)
	if contextName != "" {
		if _, ok := c.contexts[contextName]; !ok {
			return "", appValidationError(fmt.Sprintf("kubeconfig context %q was not found", contextName))
		}
		return contextName, nil
	}
	if c.currentContext != "" {
		if _, ok := c.contexts[c.currentContext]; !ok {
			return "", appValidationError(fmt.Sprintf("kubeconfig current-context %q was not found", c.currentContext))
		}
		return c.currentContext, nil
	}
	if len(c.contextNames) == 1 {
		return c.contextNames[0], nil
	}
	return "", appValidationError("kubeconfig_context is required when kubeconfig has multiple contexts and no current-context")
}

func (c parsedKubeconfig) toCluster(contextName string) (domain.Cluster, error) {
	contextName, err := c.resolveContextName(contextName)
	if err != nil {
		return domain.Cluster{}, err
	}

	contextConfig := c.contexts[contextName]
	clusterName := strings.TrimSpace(contextConfig.Cluster)
	userName := strings.TrimSpace(contextConfig.User)
	clusterConfig, ok := c.clusters[clusterName]
	if !ok {
		return domain.Cluster{}, appValidationError(fmt.Sprintf("kubeconfig context %q references missing cluster %q", contextName, clusterName))
	}
	userConfig, ok := c.users[userName]
	if !ok {
		return domain.Cluster{}, appValidationError(fmt.Sprintf("kubeconfig context %q references missing user %q", contextName, userName))
	}

	caCertPEM, err := caCertFromKubeconfig(clusterConfig, contextName)
	if err != nil {
		return domain.Cluster{}, err
	}
	auth, err := authFromKubeconfig(userConfig, contextName)
	if err != nil {
		return domain.Cluster{}, err
	}
	impersonateExtra, err := jsonString(userConfig.ImpersonateExtra, contextName, "as-user-extra")
	if err != nil {
		return domain.Cluster{}, err
	}

	return domain.Cluster{
		Name:                contextName,
		APIEndpoint:         strings.TrimSpace(clusterConfig.Server),
		AuthType:            auth.authType,
		UpstreamBearerToken: auth.token,
		CACertPEM:           caCertPEM,
		ClientCertPEM:       auth.clientCertPEM,
		ClientKeyPEM:        auth.clientKeyPEM,
		Username:            auth.username,
		Password:            auth.password,
		AuthProviderConfig:  auth.authProviderConfig,
		ExecConfig:          auth.execConfig,
		KubeconfigRaw:       c.raw,
		TLSServerName:       strings.TrimSpace(clusterConfig.TLSServerName),
		SkipTLSVerify:       clusterConfig.InsecureSkipTLSVerify,
		ProxyURL:            strings.TrimSpace(clusterConfig.ProxyURL),
		DisableCompression:  clusterConfig.DisableCompression,
		ImpersonateUser:     strings.TrimSpace(userConfig.ImpersonateUser),
		ImpersonateUID:      strings.TrimSpace(userConfig.ImpersonateUID),
		ImpersonateGroups:   strings.Join(userConfig.ImpersonateGroups, ","),
		ImpersonateExtra:    impersonateExtra,
		Namespace:           strings.TrimSpace(contextConfig.Namespace),
		SourceContext:       contextName,
		SourceCluster:       clusterName,
		SourceUser:          userName,
	}, nil
}

func (c parsedKubeconfig) toClusters(contextNames []string, defaultContext string, enabled bool, skipUnsupported bool) ([]domain.Cluster, []string, error) {
	selectedNames, err := c.selectedContextNames(contextNames)
	if err != nil {
		return nil, nil, err
	}
	defaultContext = strings.TrimSpace(defaultContext)
	if defaultContext == "" {
		defaultContext = c.currentContext
	}
	if defaultContext != "" {
		if _, ok := c.contexts[defaultContext]; !ok {
			return nil, nil, appValidationError(fmt.Sprintf("kubeconfig default_context %q was not found", defaultContext))
		}
		if !containsContextName(selectedNames, defaultContext) {
			return nil, nil, appValidationError(fmt.Sprintf("kubeconfig default_context %q must be included in context_names", defaultContext))
		}
	}

	clusters := make([]domain.Cluster, 0, len(selectedNames))
	skipped := make([]string, 0)
	for _, contextName := range selectedNames {
		cluster, parseErr := c.toCluster(contextName)
		if parseErr != nil {
			if skipUnsupported {
				if contextName == defaultContext {
					return nil, nil, appValidationError(fmt.Sprintf("kubeconfig default_context %q is not importable", defaultContext))
				}
				skipped = append(skipped, contextName)
				continue
			}
			return nil, nil, parseErr
		}
		cluster.Default = defaultContext != "" && cluster.Name == defaultContext
		cluster.Enabled = enabled
		clusters = append(clusters, cluster)
	}
	if len(clusters) == 0 {
		return nil, skipped, appValidationError("kubeconfig did not contain any importable contexts")
	}
	return clusters, skipped, nil
}

func containsContextName(contextNames []string, target string) bool {
	for _, contextName := range contextNames {
		if contextName == target {
			return true
		}
	}
	return false
}

func (c parsedKubeconfig) selectedContextNames(contextNames []string) ([]string, error) {
	if len(contextNames) == 0 {
		return append([]string{}, c.contextNames...), nil
	}

	selected := make([]string, 0, len(contextNames))
	seen := make(map[string]struct{}, len(contextNames))
	for _, contextName := range contextNames {
		contextName = strings.TrimSpace(contextName)
		if contextName == "" {
			continue
		}
		if _, ok := seen[contextName]; ok {
			continue
		}
		if _, ok := c.contexts[contextName]; !ok {
			return nil, appValidationError(fmt.Sprintf("kubeconfig context %q was not found", contextName))
		}
		seen[contextName] = struct{}{}
		selected = append(selected, contextName)
	}
	if len(selected) == 0 {
		return nil, appValidationError("context_names must contain at least one valid context")
	}
	sort.Strings(selected)
	return selected, nil
}

func caCertFromKubeconfig(cluster kubeconfigClusterConfig, contextName string) (string, error) {
	if strings.TrimSpace(cluster.CertificateAuthorityData) == "" {
		return "", nil
	}
	decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(cluster.CertificateAuthorityData))
	if err != nil {
		return "", appValidationErrorWithCause(fmt.Sprintf("kubeconfig context %q has invalid certificate-authority-data", contextName), err)
	}
	return string(decoded), nil
}

type kubeconfigAuth struct {
	authType           string
	token              string
	clientCertPEM      string
	clientKeyPEM       string
	username           string
	password           string
	authProviderConfig string
	execConfig         string
}

func authFromKubeconfig(user kubeconfigUserAuthInfo, contextName string) (kubeconfigAuth, error) {
	token := strings.TrimSpace(user.Token)
	if token != "" {
		return kubeconfigAuth{authType: AuthTypeBearerToken, token: token}, nil
	}
	if strings.TrimSpace(user.ClientCertificateData) != "" || strings.TrimSpace(user.ClientKeyData) != "" {
		clientCertPEM, err := decodeKubeconfigData(user.ClientCertificateData, contextName, "client-certificate-data")
		if err != nil {
			return kubeconfigAuth{}, err
		}
		clientKeyPEM, err := decodeKubeconfigData(user.ClientKeyData, contextName, "client-key-data")
		if err != nil {
			return kubeconfigAuth{}, err
		}
		return kubeconfigAuth{authType: AuthTypeClientCertificate, clientCertPEM: clientCertPEM, clientKeyPEM: clientKeyPEM}, nil
	}
	if strings.TrimSpace(user.Username) != "" || strings.TrimSpace(user.Password) != "" {
		return kubeconfigAuth{authType: AuthTypeBasic, username: strings.TrimSpace(user.Username), password: strings.TrimSpace(user.Password)}, nil
	}
	if len(user.AuthProvider) > 0 {
		authProviderConfig, err := jsonString(user.AuthProvider, contextName, "auth-provider")
		if err != nil {
			return kubeconfigAuth{}, err
		}
		return kubeconfigAuth{authType: AuthTypeAuthProvider, authProviderConfig: authProviderConfig}, nil
	}
	if len(user.Exec) > 0 {
		execConfig, err := jsonString(user.Exec, contextName, "exec")
		if err != nil {
			return kubeconfigAuth{}, err
		}
		return kubeconfigAuth{authType: AuthTypeExec, execConfig: execConfig}, nil
	}
	if strings.TrimSpace(user.TokenFile) != "" {
		return kubeconfigAuth{authType: AuthTypeBearerToken}, nil
	}
	if strings.TrimSpace(user.ClientCertificate) != "" || strings.TrimSpace(user.ClientKey) != "" {
		return kubeconfigAuth{authType: AuthTypeClientCertificate}, nil
	}
	return kubeconfigAuth{authType: AuthTypeBearerToken}, nil
}

func decodeKubeconfigData(value string, contextName string, field string) (string, error) {
	if strings.TrimSpace(value) == "" {
		return "", nil
	}
	decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(value))
	if err != nil {
		return "", appValidationErrorWithCause(fmt.Sprintf("kubeconfig context %q has invalid %s", contextName, field), err)
	}
	return string(decoded), nil
}

func jsonString(value map[string]any, contextName string, field string) (string, error) {
	if len(value) == 0 {
		return "", nil
	}
	payload, err := json.Marshal(value)
	if err != nil {
		return "", appValidationErrorWithCause(fmt.Sprintf("kubeconfig context %q has invalid %s", contextName, field), err)
	}
	return string(payload), nil
}
