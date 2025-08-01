package proxy

import (
	"fmt"
	"ghproxy/config"
	"net/url"
	"regexp"
	"strings"
	"sync"
)

var (
	githubPrefixLen      int
	rawPrefixLen         int
	gistPrefixLen        int
	gistContentPrefixLen int
	apiPrefixLen         int
)

const (
	githubPrefix            = "https://github.com/"
	rawPrefix               = "https://raw.githubusercontent.com/"
	gistPrefix              = "https://gist.github.com/"
	gistContentPrefix       = "https://gist.githubusercontent.com/"
	apiPrefix               = "https://api.github.com/"
	releasesDownloadSnippet = "releases/download/"
)

func init() {
	githubPrefixLen = len(githubPrefix)
	rawPrefixLen = len(rawPrefix)
	gistPrefixLen = len(gistPrefix)
	gistContentPrefixLen = len(gistContentPrefix)
	apiPrefixLen = len(apiPrefix)
}

// Matcher 从原始URL路径中高效地解析并匹配代理规则.
func Matcher(rawPath string, cfg *config.Config) (string, string, string, *GHProxyErrors) {
	if len(rawPath) < 18 {
		return "", "", "", NewErrorWithStatusLookup(404, "path too short")
	}

	// 匹配 "https://github.com/"
	if strings.HasPrefix(rawPath, githubPrefix) {
		remaining := rawPath[githubPrefixLen:]
		i := strings.IndexByte(remaining, '/')
		if i <= 0 {
			return "", "", "", NewErrorWithStatusLookup(400, "malformed github path: missing user")
		}
		user := remaining[:i]
		remaining = remaining[i+1:]
		i = strings.IndexByte(remaining, '/')
		if i <= 0 {
			return "", "", "", NewErrorWithStatusLookup(400, "malformed github path: missing repo")
		}
		repo := remaining[:i]
		remaining = remaining[i+1:]
		if len(remaining) == 0 {
			return "", "", "", NewErrorWithStatusLookup(400, "malformed github path: missing action")
		}
		i = strings.IndexByte(remaining, '/')
		action := remaining
		if i != -1 {
			action = remaining[:i]
		}
		var matcher string
		switch action {
		case "releases":
			if strings.HasPrefix(remaining, releasesDownloadSnippet) {
				matcher = "releases"
			} else {
				return "", "", "", NewErrorWithStatusLookup(400, "malformed github path: not a releases download url")
			}
		case "archive":
			matcher = "releases"
		case "blob":
			matcher = "blob"
		case "raw":
			matcher = "raw"
		case "info", "git-upload-pack":
			matcher = "clone"
		default:
			return "", "", "", NewErrorWithStatusLookup(400, fmt.Sprintf("unsupported github action: %s", action))
		}
		return user, repo, matcher, nil
	}

	// 匹配 "https://raw.githubusercontent.com/"
	if strings.HasPrefix(rawPath, rawPrefix) {
		remaining := rawPath[rawPrefixLen:]
		// 这里的逻辑与 github.com 的类似, 需要提取 user, repo, branch, file...
		// 我们只需要 user 和 repo
		i := strings.IndexByte(remaining, '/')
		if i <= 0 {
			return "", "", "", NewErrorWithStatusLookup(400, "malformed raw url: missing user")
		}
		user := remaining[:i]
		remaining = remaining[i+1:]
		i = strings.IndexByte(remaining, '/')
		if i <= 0 {
			return "", "", "", NewErrorWithStatusLookup(400, "malformed raw url: missing repo")
		}
		repo := remaining[:i]
		// raw 链接至少需要 user/repo/branch 三部分
		remaining = remaining[i+1:]
		if len(remaining) == 0 {
			return "", "", "", NewErrorWithStatusLookup(400, "malformed raw url: missing branch/commit")
		}
		return user, repo, "raw", nil
	}

	// 匹配 "https://gist.github.com/"
	if strings.HasPrefix(rawPath, gistPrefix) {
		remaining := rawPath[gistPrefixLen:]
		i := strings.IndexByte(remaining, '/')
		if i <= 0 {
			// case: https://gist.github.com/user
			// 这种情况下, gist_id 缺失, 但我们仍然可以认为 user 是有效的
			if len(remaining) > 0 {
				return remaining, "", "gist", nil
			}
			return "", "", "", NewErrorWithStatusLookup(400, "malformed gist url: missing user")
		}
		// case: https://gist.github.com/user/gist_id...
		user := remaining[:i]
		return user, "", "gist", nil
	}

	// 匹配 "https://gist.githubusercontent.com/"
	if strings.HasPrefix(rawPath, gistContentPrefix) {
		remaining := rawPath[gistContentPrefixLen:]
		i := strings.IndexByte(remaining, '/')
		if i <= 0 {
			// case: https://gist.githubusercontent.com/user
			// 这种情况下, gist_id 缺失, 但我们仍然可以认为 user 是有效的
			if len(remaining) > 0 {
				return remaining, "", "gist", nil
			}
			return "", "", "", NewErrorWithStatusLookup(400, "malformed gist url: missing user")
		}
		// case: https://gist.githubusercontent.com/user/gist_id...
		user := remaining[:i]
		return user, "", "gist", nil
	}

	// 匹配 "https://api.github.com/"
	if strings.HasPrefix(rawPath, apiPrefix) {
		if !cfg.Auth.ForceAllowApi && (cfg.Auth.Method != "header" || !cfg.Auth.Enabled) {
			return "", "", "", NewErrorWithStatusLookup(403, "API proxy requires header authentication")
		}
		remaining := rawPath[apiPrefixLen:]
		var user, repo string
		if strings.HasPrefix(remaining, "repos/") {
			parts := strings.SplitN(remaining[6:], "/", 3)
			if len(parts) >= 2 {
				user = parts[0]
				repo = parts[1]
			}
		} else if strings.HasPrefix(remaining, "users/") {
			parts := strings.SplitN(remaining[6:], "/", 2)
			if len(parts) >= 1 {
				user = parts[0]
			}
		}
		return user, repo, "api", nil
	}

	return "", "", "", NewErrorWithStatusLookup(404, "no matcher found for the given path")
}

var (
	proxyableMatchersMap map[string]struct{}
	initMatchersOnce     sync.Once
)

func initMatchers() {
	initMatchersOnce.Do(func() {
		matchers := []string{"blob", "raw", "gist"}
		proxyableMatchersMap = make(map[string]struct{}, len(matchers))
		for _, m := range matchers {
			proxyableMatchersMap[m] = struct{}{}
		}
	})
}

// matchString 与原始版本签名兼容
func matchString(target string) bool {
	initMatchers()
	_, exists := proxyableMatchersMap[target]
	return exists
}

// extractParts 与原始版本签名兼容
func extractParts(rawURL string) (string, string, string, url.Values, error) {
	parsedURL, err := url.Parse(rawURL)
	if err != nil {
		return "", "", "", nil, err
	}

	path := parsedURL.Path
	if len(path) > 0 && path[0] == '/' {
		path = path[1:]
	}

	parts := strings.SplitN(path, "/", 3)

	if len(parts) < 2 {
		return "", "", "", nil, fmt.Errorf("URL path is too short")
	}

	repoOwner := "/" + parts[0]
	repoName := "/" + parts[1]
	var remainingPath string
	if len(parts) > 2 {
		remainingPath = "/" + parts[2]
	}

	return repoOwner, repoName, remainingPath, parsedURL.Query(), nil
}

var urlPattern = regexp.MustCompile(`https?://[^\s'"]+`)
