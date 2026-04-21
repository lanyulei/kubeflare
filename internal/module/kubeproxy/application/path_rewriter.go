package application

import (
	"fmt"
	"strings"
)

const (
	kapiPrefix  = "/kapi"
	kapisPrefix = "/kapis"
)

func RewritePath(path string) (string, error) {
	switch {
	case path == kapiPrefix:
		return "/api", nil
	case strings.HasPrefix(path, kapiPrefix+"/"):
		return "/api/" + strings.TrimPrefix(path, kapiPrefix+"/"), nil
	case path == kapisPrefix:
		return "/apis", nil
	case strings.HasPrefix(path, kapisPrefix+"/"):
		return "/apis/" + strings.TrimPrefix(path, kapisPrefix+"/"), nil
	default:
		return "", fmt.Errorf("unsupported kubernetes proxy path %q", path)
	}
}
